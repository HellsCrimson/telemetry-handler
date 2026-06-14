package lmu

import "testing"

func TestLooksLikePacket(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"json", []byte(`{"source":"lmu"}`), true},
		{"empty", nil, false},
		{"binary", []byte{0x01, 0x00, 0x00, 0x00}, false},
		{"leading space", []byte(` {`), false},
	}
	for _, c := range cases {
		if got := LooksLikePacket(c.data); got != c.want {
			t.Errorf("%s: LooksLikePacket = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	data := []byte(`{"source":"lmu","seq":42,"num_vehicles":18,"vehicle_name":"GT3 #91",` +
		`"gear":3,"engine_rpm":7500,"engine_max_rpm":9000,"speed_ms":55.5,` +
		`"throttle":1,"brake":0,"steering":-0.25,"clutch":0,"fuel":63.2}`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Seq != 42 || p.VehicleName != "GT3 #91" || p.Gear != 3 {
		t.Errorf("unexpected header fields: %+v", p)
	}
	if p.EngineRPM != 7500 || p.EngineMaxRPM != 9000 || p.SpeedMS != 55.5 {
		t.Errorf("unexpected numeric fields: %+v", p)
	}
	if p.Throttle != 1 || p.Steering != -0.25 || p.Fuel != 63.2 {
		t.Errorf("unexpected input fields: %+v", p)
	}
}

func TestParseRejectsNonLMU(t *testing.T) {
	if _, err := Parse([]byte(`{"source":"forza"}`)); err == nil {
		t.Fatal("expected error for non-lmu source")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid json")
	}
}
