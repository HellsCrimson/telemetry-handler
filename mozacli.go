package main

import (
	"fmt"

	"telemetry-handler/config"
	"telemetry-handler/wheelbase/moza"
)

// runMozaBase is the headless bring-up harness for wheelbase settings, mirroring
// the -moza-test pattern. With no probe key it reads everything and writes
// nothing; with one it runs a single self-restoring write test.
//
// It talks to the wheel directly rather than through the running app, so it can
// be used before the settings page is trusted — and so a probe is never
// competing with LED streaming for the port.
func runMozaBase(cfg config.Config, portOverride, probeKey string, probeTo int) error {
	port := portOverride
	if port == "" {
		port = cfg.Moza.Port
	}
	if port == "" {
		// Fall back to whatever USB detection finds, so the common case needs no
		// flags at all.
		if devices, err := moza.Detect(); err == nil && len(devices) > 0 {
			port = devices[0].Port
		}
	}
	if port == "" {
		return fmt.Errorf("no MOZA serial port configured or detected (use -moza-port)")
	}

	return moza.WithBase(nil, port, func(c *moza.BaseClient) error {
		switch probeKey {
		case "":
			return printBaseSettings(c)
		case "all":
			return sweepBaseSettings(c)
		case "apply":
			return probeGroupedApply(c)
		default:
			return probeBaseSetting(c, probeKey, probeTo)
		}
	})
}

// printBaseSettings dumps the wheelbase's stored configuration.
func printBaseSettings(c *moza.BaseClient) error {
	if status, err := c.ReadStatus(); err == nil {
		for key, celsius := range status.Temps {
			fmt.Printf("%-24s %.1f C\n", key, celsius)
		}
		if status.HasState {
			fmt.Printf("%-24s %d\n", "state", status.State)
		}
		if status.HasError {
			fmt.Printf("%-24s %d\n", "state_err", status.Error)
		}
		fmt.Println()
	}

	snap, err := c.ReadAllSettings()
	if err != nil {
		return err
	}
	for _, cmd := range moza.BaseCommands() {
		if snap.Unsupported[cmd.Key] {
			fmt.Printf("%-24s %-8s %s\n", cmd.Key, "—", "no reply (may not be supported)")
			continue
		}
		value, ok := snap.Values[cmd.Key]
		if !ok {
			continue
		}
		note := ""
		if !cmd.Verified {
			note = "(conversion unconfirmed)"
		}
		fmt.Printf("%-24s %-8d %s %s\n", cmd.Key, value, cmd.Unit, note)
	}
	return nil
}

// sweepBaseSettings probes the whole first-write set, one setting at a time,
// restoring each before moving to the next.
func sweepBaseSettings(c *moza.BaseClient) error {
	keys := moza.FirstWriteSet()
	fmt.Printf("probing %d settings, one at a time, restoring each\n\n", len(keys))

	result, err := moza.ProbeSweep(c, keys)
	for _, report := range result.Reports {
		fmt.Printf("%s\n%s\n", report.Key, report)
	}
	for _, skip := range result.Skipped {
		fmt.Printf("%-24s skipped: %s\n", skip.Key, skip.Reason)
	}
	if err != nil {
		return err
	}

	fmt.Printf("\n%d probed, %d skipped\n", len(result.Reports), len(result.Skipped))
	if !result.Ok() {
		return fmt.Errorf("not every probe succeeded — see the steps above")
	}
	fmt.Println("every probe wrote, read back and restored")
	return nil
}

// probeGroupedApply write-tests the grouped apply — the path the settings page
// will use — rather than one setting at a time.
//
// This is the step that matters before the UI is allowed to write: back-to-back
// requests are what overrun the base, and a dropped write is invisible unless
// something reads the values back.
func probeGroupedApply(c *moza.BaseClient) error {
	keys := moza.FirstWriteSet()
	fmt.Printf("lowering %d settings in ONE apply, reading them all back, then restoring\n\n", len(keys))

	report, err := moza.ProbeApply(c, keys)
	fmt.Print(report)
	for _, skip := range report.Skipped {
		fmt.Printf("%-24s skipped: %s\n", skip.Key, skip.Reason)
	}
	fmt.Println()
	for _, key := range report.Order {
		fmt.Printf("%-24s %d -> %d, read back %d\n", key, report.Originals[key], report.Targets[key], report.ReadBack[key])
	}
	if err != nil {
		return err
	}

	switch {
	case !report.Restored:
		// The one outcome that needs a person: the wheel is not as it was found.
		return fmt.Errorf("these settings were NOT restored: %v — check the wheel before driving",
			report.Unrestored)
	case len(report.Failed) > 0:
		return fmt.Errorf("%d write(s) failed: %v", len(report.Failed), report.Failed)
	case len(report.Mismatched) > 0:
		return fmt.Errorf("%d setting(s) did not read back as written: %v — a write was dropped, "+
			"which means the pacing between writes is not yet enough", len(report.Mismatched), report.Mismatched)
	}
	fmt.Println("\nthe grouped apply wrote every setting, all read back, and all were restored")
	return nil
}

// probeBaseSetting runs the single self-restoring write test.
//
// The default target is half the current value: comfortably lower, obviously
// different when you turn the wheel, and never a raise.
func probeBaseSetting(c *moza.BaseClient, key string, target int) error {
	cmd, ok := moza.LookupBaseCommand(key)
	if !ok {
		return fmt.Errorf("unknown setting %q — run -moza-base-read to list them", key)
	}
	if target < 0 {
		current, err := c.ReadSetting(cmd)
		if err != nil {
			return fmt.Errorf("read %s: %w", key, err)
		}
		target = current / 2
		if target >= current {
			return fmt.Errorf("%s is %d, which leaves nothing to lower to; pass -moza-base-probe-to", key, current)
		}
	}

	fmt.Printf("probing %s (%s)\n", key, cmd.Name)
	report, err := moza.ProbeLower(c, key, target)
	fmt.Print(report)
	if err != nil {
		return err
	}

	switch {
	case !report.Restored:
		// The one outcome that needs a person: the wheel is not as it was found.
		return fmt.Errorf("the original value was NOT restored — check the wheel before driving")
	case !report.Verified:
		return fmt.Errorf("the base reported %d after writing %d: the write did not take, "+
			"or the conversion for this setting is wrong", report.ReadBack, report.Target)
	}
	fmt.Println("write, read back and restore all succeeded")
	return nil
}
