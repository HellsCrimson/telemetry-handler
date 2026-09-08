package app

import (
	"strings"
	"testing"

	"telemetry-handler/config"
)

// Every refusal below must happen before anything reaches the wire. A patch that
// is half-applied and then rejected leaves the wheel in a state neither the user
// nor the app asked for, so these are checked up front rather than per write.
//
// None of these tests need hardware: each is refused before a port is opened.

func writableRuntime() *Runtime {
	cfg := config.Default()
	cfg.Moza.AllowBaseWrites = true
	cfg.Moza.Port = "/dev/null/not-a-port"
	return NewRuntime(cfg, "", nil, nil)
}

// The gate itself. Wheelbase settings persist on the hardware, so the app does
// not own them until the config says it does.
func TestApplyMozaBaseRefusesWhenWritesAreDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Moza.Port = "/dev/null/not-a-port"
	r := NewRuntime(cfg, "", nil, nil)

	res := r.ApplyMozaBase(map[string]int{"ffb_strength": 50})
	if res.Ok {
		t.Fatal("a write must not succeed while writing is disabled")
	}
	if !strings.Contains(res.Refused, "allow_base_writes") {
		t.Errorf("the refusal should name the config key, got %q", res.Refused)
	}
	if len(res.Applied) != 0 {
		t.Errorf("nothing should have been applied, got %v", res.Applied)
	}
}

func TestApplyMozaBaseRefusesAnUnknownSetting(t *testing.T) {
	res := writableRuntime().ApplyMozaBase(map[string]int{"ffb_strength": 50, "not_a_setting": 1})
	if !strings.Contains(res.Refused, "not_a_setting") {
		t.Errorf("refusal = %q, want it to name the unknown setting", res.Refused)
	}
}

// An out-of-range value is refused for the whole patch, not just for itself: the
// registry the UI built its controls from is the same one that validates here,
// so a value that gets this far means the two are out of step and the rest of the
// patch is no longer trustworthy either.
func TestApplyMozaBaseRefusesAnOutOfRangeValue(t *testing.T) {
	res := writableRuntime().ApplyMozaBase(map[string]int{"torque": 250})
	if res.Ok || res.Refused == "" {
		t.Fatalf("250%% torque must be refused, got %+v", res)
	}
}

// The same guard the write probe applies: an unconfirmed conversion means we do
// not know what number actually reaches the base, and "write 20" arriving as 200
// on a torque cap is exactly the accident this prevents.
func TestApplyMozaBaseRefusesUnverifiedConversions(t *testing.T) {
	var unverified string
	for _, cmd := range writableRuntime().MozaBaseCommands() {
		if !cmd.Verified {
			unverified = cmd.Key
			break
		}
	}
	if unverified == "" {
		t.Skip("every conversion is confirmed now")
	}
	res := writableRuntime().ApplyMozaBase(map[string]int{unverified: 1})
	if !strings.Contains(res.Refused, "not confirmed") {
		t.Errorf("refusal = %q, want it to say the conversion is unconfirmed", res.Refused)
	}
}

func TestApplyMozaBaseRefusesAnEmptyPatch(t *testing.T) {
	if res := writableRuntime().ApplyMozaBase(nil); res.Refused == "" {
		t.Error("an empty patch should be refused rather than opening the port for nothing")
	}
}

// The snapshot carries the permission, so the page has one authoritative answer
// rather than inferring it from the config.
func TestReadMozaBaseReportsWritePermission(t *testing.T) {
	writable, reason := writableRuntime().mozaWritePermission()
	if !writable || reason != "" {
		t.Errorf("writable=%v reason=%q, want permitted with no reason", writable, reason)
	}
	writable, reason = NewRuntime(config.Default(), "", nil, nil).mozaWritePermission()
	if writable {
		t.Error("writing must be off by default")
	}
	if !strings.Contains(reason, "allow_base_writes") {
		t.Errorf("reason = %q, want it to name the config key", reason)
	}
}
