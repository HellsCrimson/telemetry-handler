package moza

import (
	"fmt"
	"strings"
)

// The first write.
//
// Everything up to now has been read-only. This is the bring-up harness for the
// first setting the app ever writes to a wheelbase, and it is deliberately a
// narrow, self-restoring probe rather than a general write path:
//
//	read the current value -> write a LOWER one -> read it back -> restore
//
// Three properties make it safe to run on a wheel you are sitting behind, and
// all three are enforced here rather than left to the operator:
//
//   - It only ever lowers. Reducing force feedback, torque or angle cannot
//     surprise a driver the way raising them can, and a probe that could raise
//     them would need supervision this one does not.
//   - It only probes settings whose conversion is confirmed. An unverified
//     scale means we do not actually know what number reaches the hardware, and
//     "write 20" landing as 200 is precisely the accident to avoid.
//   - It always restores, including when the verification step fails. The
//     wheel must not be left altered because a read timed out.

// ProbeReport records what a probe did, step by step, so a failure says which
// stage it reached rather than just that something went wrong.
type ProbeReport struct {
	Key      string
	Original int
	Target   int
	// ReadBack is what the base reported after the write.
	ReadBack int
	// Verified is true when ReadBack matched Target.
	Verified bool
	// Restored is true when the original value was put back and confirmed.
	// A false here on an otherwise successful probe is the one outcome that
	// needs a human: the wheel is not as it was found.
	Restored     bool
	RestoreValue int
	Steps        []string
}

func (r *ProbeReport) step(format string, args ...any) {
	r.Steps = append(r.Steps, fmt.Sprintf(format, args...))
}

// String renders the report as the CLI prints it.
func (r ProbeReport) String() string {
	var b strings.Builder
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	return b.String()
}

// ProbeLower reads a setting, writes a lower value, reads it back, and restores
// the original.
//
// target must be strictly below the current value. Passing a target at or above
// it is refused rather than clamped: a probe that silently did something other
// than what was asked would defeat the point of running one.
// The return values are named because the restore runs in a defer and records
// its outcome on the report: with unnamed returns the caller would receive a
// copy taken before the restore ran, and never learn whether the wheel was put
// back.
func ProbeLower(c *BaseClient, key string, target int) (report ProbeReport, err error) {
	report = ProbeReport{Key: key, Target: target}

	cmd, ok := LookupBaseCommand(key)
	if !ok {
		return report, fmt.Errorf("unknown setting %q", key)
	}
	if !cmd.Verified {
		return report, fmt.Errorf("%s: conversion is not confirmed on hardware, so a written value "+
			"cannot be trusted to mean what it says — probe a verified setting instead", key)
	}
	if err := cmd.Validate(target); err != nil {
		return report, err
	}

	original, err := c.ReadSetting(cmd)
	if err != nil {
		return report, fmt.Errorf("read current value: %w", err)
	}
	report.Original = original
	report.step("read    %s = %d%s", key, original, unitOf(cmd))

	// If the current value is outside the range this command declares, the
	// restore at the end would fail validation and the wheel would be left at the
	// probe value. Refuse before writing anything, and say which is wrong — the
	// declared range or the conversion.
	if err := cmd.Validate(original); err != nil {
		return report, fmt.Errorf("%s currently reads %d%s, which is outside its declared range %d..%d: "+
			"either the range or the conversion is wrong, and the original could not be restored after a write (%w)",
			key, original, unitOf(cmd), cmd.Min, cmd.Max, err)
	}

	if target >= original {
		return report, fmt.Errorf("%s is %d%s and the probe only lowers: choose a target below it",
			key, original, unitOf(cmd))
	}

	if err := c.WriteSetting(cmd, target); err != nil {
		return report, fmt.Errorf("write %d: %w", target, err)
	}
	report.step("wrote   %s = %d%s", key, target, unitOf(cmd))

	// Restore no matter what happens next, including a failed read back. Leaving
	// the wheel quieter than it was found is still leaving it changed.
	defer func() {
		if err := c.WriteSetting(cmd, original); err != nil {
			report.step("RESTORE FAILED: %v — the wheel is still at %d%s", err, target, unitOf(cmd))
			return
		}
		back, err := c.ReadSetting(cmd)
		if err != nil {
			report.step("restored %s = %d%s (could not confirm: %v)", key, original, unitOf(cmd), err)
			return
		}
		report.RestoreValue = back
		report.Restored = back == original
		if report.Restored {
			report.step("restored %s = %d%s", key, back, unitOf(cmd))
		} else {
			report.step("RESTORE MISMATCH: %s reads %d%s, expected %d%s", key, back, unitOf(cmd), original, unitOf(cmd))
		}
	}()

	readBack, err := c.ReadSetting(cmd)
	if err != nil {
		return report, fmt.Errorf("read back after write: %w", err)
	}
	report.ReadBack = readBack
	report.Verified = readBack == target
	if report.Verified {
		report.step("read    %s = %d%s — matches what was written", key, readBack, unitOf(cmd))
	} else {
		report.step("MISMATCH: wrote %d%s, base reports %d%s", target, unitOf(cmd), readBack, unitOf(cmd))
	}
	return report, nil
}

func unitOf(c BaseCommand) string {
	if c.Unit == "" {
		return ""
	}
	return " " + c.Unit
}
