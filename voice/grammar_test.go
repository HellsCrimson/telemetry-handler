package voice

import "testing"

func TestParseFuel(t *testing.T) {
	tests := []struct {
		text    string
		liters  float64
		set     bool
		max     bool
	}{
		{"fuel to 30", 30, true, false},
		{"add 45 litres of fuel", 45, true, false},
		{"fuel thirty five", 35, true, false},
		{"fill it up", 0, false, true},
		{"full fuel", 0, false, true},
		{"no fuel", 0, true, false},
	}
	for _, tc := range tests {
		u := Parse(tc.text)
		if len(u.Actions) != 1 || u.Actions[0].Type != ActFuel {
			t.Fatalf("%q: expected one fuel action, got %+v", tc.text, u.Actions)
		}
		a := u.Actions[0]
		if a.FuelMax != tc.max || a.FuelSet != tc.set || (tc.set && a.Liters != tc.liters) {
			t.Errorf("%q: got liters=%v set=%v max=%v, want liters=%v set=%v max=%v",
				tc.text, a.Liters, a.FuelSet, a.FuelMax, tc.liters, tc.set, tc.max)
		}
	}
}

func TestParseEnergy(t *testing.T) {
	tests := []struct {
		text   string
		pct    float64
		set    bool
		max    bool
	}{
		{"energy to 50", 50, true, false},
		{"virtual energy 80 percent", 80, true, false},
		{"full energy", 0, false, true},
	}
	for _, tc := range tests {
		u := Parse(tc.text)
		if len(u.Actions) != 1 || u.Actions[0].Type != ActEnergy {
			t.Fatalf("%q: expected one energy action, got %+v", tc.text, u.Actions)
		}
		a := u.Actions[0]
		if a.FuelMax != tc.max || a.FuelSet != tc.set || (tc.set && a.Liters != tc.pct) {
			t.Errorf("%q: got pct=%v set=%v max=%v, want pct=%v set=%v max=%v",
				tc.text, a.Liters, a.FuelSet, a.FuelMax, tc.pct, tc.set, tc.max)
		}
	}
}

func TestParseTyres(t *testing.T) {
	tests := []struct {
		text     string
		which    TyreSel
		noTyres  bool
		compound string
	}{
		{"change all four tyres", TyreAll, false, ""},
		{"front tyres only", TyreFront, false, ""},
		{"rear tyres", TyreRear, false, ""},
		{"change tyres", TyreAll, false, ""},
		{"don't change tyres", TyreUnset, true, ""},
		{"all tyres soft", TyreAll, false, "soft"},
		{"put on wets", TyreUnset, false, "wet"},
	}
	for _, tc := range tests {
		u := Parse(tc.text)
		if len(u.Actions) != 1 || u.Actions[0].Type != ActTyres {
			t.Fatalf("%q: expected one tyre action, got %+v", tc.text, u.Actions)
		}
		a := u.Actions[0]
		if a.Which != tc.which || a.NoTyres != tc.noTyres || a.Compound != tc.compound {
			t.Errorf("%q: got which=%v no=%v compound=%q, want which=%v no=%v compound=%q",
				tc.text, a.Which, a.NoTyres, a.Compound, tc.which, tc.noTyres, tc.compound)
		}
	}
}

func TestParsePit(t *testing.T) {
	on := []string{"box box", "box this lap", "pit this lap", "request a pit stop", "come in"}
	for _, text := range on {
		u := Parse(text)
		if len(u.Actions) != 1 || u.Actions[0].Type != ActPit || !u.Actions[0].PitOn {
			t.Errorf("%q: expected pit-request action, got %+v", text, u.Actions)
		}
	}
	off := []string{"cancel pit", "don't pit", "stay out"}
	for _, text := range off {
		u := Parse(text)
		if len(u.Actions) != 1 || u.Actions[0].Type != ActPit || u.Actions[0].PitOn {
			t.Errorf("%q: expected pit-cancel action, got %+v", text, u.Actions)
		}
	}
}

func TestParseMultipleActions(t *testing.T) {
	u := Parse("box this lap, fuel to 30 and change all tyres")
	if len(u.Actions) != 3 {
		t.Fatalf("expected 3 actions, got %d: %+v", len(u.Actions), u.Actions)
	}
}

func TestParseAffirmCancel(t *testing.T) {
	for _, w := range []string{"yes", "confirm", "do it", "okay", "yep"} {
		if u := Parse(w); !u.Affirm || u.Cancel || len(u.Actions) != 0 {
			t.Errorf("%q: expected affirm, got %+v", w, u)
		}
	}
	for _, w := range []string{"no", "cancel", "stop", "abort"} {
		if u := Parse(w); !u.Cancel || u.Affirm || len(u.Actions) != 0 {
			t.Errorf("%q: expected cancel, got %+v", w, u)
		}
	}
}

func TestParseCommandBeatsAffirm(t *testing.T) {
	// A transcript naming a command is a fresh command even if it contains "yes".
	u := Parse("yes change all tyres")
	if u.Affirm {
		t.Errorf("expected command, not affirm: %+v", u)
	}
	if len(u.Actions) != 1 || u.Actions[0].Type != ActTyres {
		t.Errorf("expected tyre action, got %+v", u.Actions)
	}
}

func TestParseEmptyOrGarbled(t *testing.T) {
	for _, text := range []string{"", "   ", "the quick brown fox"} {
		u := Parse(text)
		if len(u.Actions) != 0 || u.Affirm || u.Cancel {
			t.Errorf("%q: expected nothing actionable, got %+v", text, u)
		}
	}
}
