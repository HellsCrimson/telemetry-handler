package moza

import (
	"fmt"
	"sort"
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

// uniqueSorted tidies a list a probe built from two passes, so a setting that
// both failed to write and failed to confirm is named once.
func uniqueSorted(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func unitOf(c BaseCommand) string {
	if c.Unit == "" {
		return ""
	}
	return " " + c.Unit
}

// FirstWriteSet is the set of settings validated before the app is allowed to
// write anything on its own.
//
// Deliberately excludes calibration, FFB disable, the startup chime and anything
// in the music/calibration group. Each is either irreversible, a safety control
// in its own right, or shares a group with one.
func FirstWriteSet() []string {
	return []string{
		"limit_angle", "torque", "speed", // the safety envelope
		"ffb_strength",
		"damper", "friction", "spring", "inertia",
		"speed_damping",
	}
}

// SkipReason explains why a setting could not be probed. It is not a failure:
// a boolean already at zero has nothing lower to write, and an unverified
// conversion must not be written at all.
type SkipReason struct {
	Key    string
	Reason string
}

// ProbeSetResult is the outcome of sweeping several settings.
type ProbeSetResult struct {
	Reports []ProbeReport
	Skipped []SkipReason
	// Aborted names the setting that stopped the sweep, if one did.
	Aborted string
}

// Ok reports whether every attempted probe wrote, verified and restored.
func (r ProbeSetResult) Ok() bool {
	if r.Aborted != "" {
		return false
	}
	for _, report := range r.Reports {
		if !report.Verified || !report.Restored {
			return false
		}
	}
	return true
}

// ProbeSweep probes each setting in turn, restoring every one before moving on.
//
// It stops at the first probe that leaves the wheel unrestored. Continuing would
// pile changes onto a base already in an unknown state, and the operator needs
// to look at it before anything else is written.
func ProbeSweep(c *BaseClient, keys []string) (ProbeSetResult, error) {
	var out ProbeSetResult

	for _, key := range keys {
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			out.Skipped = append(out.Skipped, SkipReason{key, "not in the registry"})
			continue
		}
		if !cmd.Verified {
			out.Skipped = append(out.Skipped, SkipReason{key, "conversion not confirmed on hardware"})
			continue
		}

		current, err := c.ReadSetting(cmd)
		if err != nil {
			out.Skipped = append(out.Skipped, SkipReason{key, "could not read: " + err.Error()})
			continue
		}
		target, ok := lowerTarget(cmd, current)
		if !ok {
			out.Skipped = append(out.Skipped, SkipReason{key,
				fmt.Sprintf("already at %d, its lowest value — nothing lower to write", current)})
			continue
		}

		report, err := ProbeLower(c, key, target)
		out.Reports = append(out.Reports, report)
		if err != nil {
			out.Aborted = key
			return out, fmt.Errorf("probing %s: %w", key, err)
		}
		if !report.Restored {
			out.Aborted = key
			return out, fmt.Errorf("%s was not restored — stopping before anything else is written", key)
		}
	}
	return out, nil
}

// lowerTarget picks a value clearly below the current one and still inside the
// command's range. Half is obvious enough to feel through the wheel; when that
// falls below the floor, the floor itself is used.
func lowerTarget(cmd BaseCommand, current int) (int, bool) {
	if current <= cmd.Min {
		return 0, false
	}
	target := current / 2
	if target < cmd.Min {
		target = cmd.Min
	}
	if target >= current {
		target = current - 1
	}
	if target < cmd.Min {
		return 0, false
	}
	return target, true
}

// ApplyProbeReport records a grouped-apply probe: one ApplySettings lowering
// several settings at once, verified by reading them all back, then one
// ApplySettings putting the originals back.
type ApplyProbeReport struct {
	// Originals and Targets are in display units, keyed by setting.
	Originals map[string]int
	Targets   map[string]int
	// Order is the sequence ApplySettings actually wrote in, which is the point
	// of the probe as much as the values are.
	Order []string
	// ReadBack is what the base reported after the grouped write.
	ReadBack map[string]int
	// Mismatched names settings whose read-back did not equal the target. This is
	// what a dropped write looks like from the outside, and it is the failure the
	// pacing exists to prevent.
	Mismatched []string
	Failed     map[string]error
	// Restored is true when every original was written back and confirmed.
	Restored   bool
	Unrestored []string
	Skipped    []SkipReason
	Steps      []string
}

func (r *ApplyProbeReport) step(format string, args ...any) {
	r.Steps = append(r.Steps, fmt.Sprintf(format, args...))
}

// String renders the report as the CLI prints it.
func (r ApplyProbeReport) String() string {
	var b strings.Builder
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	return b.String()
}

// Ok reports whether the grouped write landed in full and the wheel was put back.
func (r ApplyProbeReport) Ok() bool {
	return len(r.Failed) == 0 && len(r.Mismatched) == 0 && r.Restored
}

// ProbeApply write-tests the grouped apply itself.
//
// ProbeSweep validates one setting at a time; this validates what the app will
// actually do — several writes back to back, which is where the base's request
// pacing is tested. A read the base drops shows up as an empty field; a WRITE it
// drops is a setting the user believes they changed and did not, so the read-back
// here is the whole point.
//
// The same three guards as ProbeLower apply: it only lowers, only touches
// settings whose conversion is confirmed, and always restores — including when
// the verification step fails.
func ProbeApply(c *BaseClient, keys []string) (report ApplyProbeReport, err error) {
	report = ApplyProbeReport{
		Originals: map[string]int{},
		Targets:   map[string]int{},
		ReadBack:  map[string]int{},
		Failed:    map[string]error{},
	}

	patch := SettingsPatch{}
	for _, key := range keys {
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			report.Skipped = append(report.Skipped, SkipReason{key, "not in the registry"})
			continue
		}
		if !cmd.Verified {
			report.Skipped = append(report.Skipped, SkipReason{key, "conversion not confirmed on hardware"})
			continue
		}
		current, err := c.ReadSetting(cmd)
		if err != nil {
			report.Skipped = append(report.Skipped, SkipReason{key, "could not read: " + err.Error()})
			continue
		}
		// A value outside its own declared range could not be restored after the
		// write, which rules the setting out of a probe entirely.
		if err := cmd.Validate(current); err != nil {
			report.Skipped = append(report.Skipped, SkipReason{key,
				fmt.Sprintf("reads %d, outside its declared range %d..%d — it could not be restored", current, cmd.Min, cmd.Max)})
			continue
		}
		target, ok := lowerTarget(cmd, current)
		if !ok {
			report.Skipped = append(report.Skipped, SkipReason{key,
				fmt.Sprintf("already at %d, its lowest value — nothing lower to write", current)})
			continue
		}
		report.Originals[key] = current
		report.Targets[key] = target
		patch[key] = target
	}

	if len(patch) == 0 {
		return report, fmt.Errorf("nothing to probe: no setting could be lowered")
	}
	report.step("read %d settings; lowering all of them in one apply", len(patch))

	// Restore in a single grouped apply too, so an interrupted probe is put back
	// by the same code path it is testing.
	defer func() {
		restore := SettingsPatch{}
		for key, original := range report.Originals {
			restore[key] = original
		}
		res, restoreErr := c.ApplySettings(restore)
		if restoreErr != nil {
			report.step("RESTORE FAILED: %v — the wheel is still at the probe values", restoreErr)
			return
		}
		for key := range restore {
			if err := res.Failed[key]; err != nil {
				report.Unrestored = append(report.Unrestored, key)
				report.step("RESTORE FAILED: %s: %v", key, err)
			}
		}
		// Confirm by reading, not by trusting the acknowledgement: the point of the
		// probe is that an acknowledged write is not proof.
		confirm := make([]string, 0, len(report.Originals))
		for key := range report.Originals {
			confirm = append(confirm, key)
		}
		sort.Strings(confirm)
		for _, key := range confirm {
			original := report.Originals[key]
			cmd, _ := LookupBaseCommand(key)
			back, err := c.ReadSetting(cmd)
			if err != nil {
				report.step("restored %s = %d (could not confirm: %v)", key, original, err)
				continue
			}
			if back != original {
				report.Unrestored = append(report.Unrestored, key)
				report.step("RESTORE MISMATCH: %s reads %d, expected %d", key, back, original)
			}
		}
		report.Unrestored = uniqueSorted(report.Unrestored)
		report.Restored = len(report.Unrestored) == 0
		if report.Restored {
			report.step("restored all %d settings", len(restore))
		}
	}()

	result, err := c.ApplySettings(patch)
	if err != nil {
		return report, fmt.Errorf("grouped apply: %w", err)
	}
	report.Order = result.Applied
	report.Failed = result.Failed
	report.step("wrote %d settings in order: %s", len(result.Applied), strings.Join(result.Applied, " "))
	for key, err := range result.Failed {
		report.step("WRITE FAILED: %s: %v", key, err)
	}

	// Sorted so the report reads the same way twice; a map range would shuffle
	// the mismatch list between runs of the same probe.
	written := make([]string, 0, len(report.Targets))
	for key := range report.Targets {
		written = append(written, key)
	}
	sort.Strings(written)

	for _, key := range written {
		target := report.Targets[key]
		cmd, _ := LookupBaseCommand(key)
		back, err := c.ReadSetting(cmd)
		if err != nil {
			report.Mismatched = append(report.Mismatched, key)
			report.step("READ BACK FAILED: %s: %v", key, err)
			continue
		}
		report.ReadBack[key] = back
		if back != target {
			report.Mismatched = append(report.Mismatched, key)
			report.step("MISMATCH: %s wrote %d, base reports %d", key, target, back)
		}
	}
	if len(report.Mismatched) == 0 && len(report.Failed) == 0 {
		report.step("all %d settings read back as written", len(report.Targets))
	}
	return report, nil
}
