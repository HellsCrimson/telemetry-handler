package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOptionalUsesDefaultsWhenDefaultFileMissing(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg, loadedPath, err := LoadOptional("")
	if err != nil {
		t.Fatalf("LoadOptional returned error: %v", err)
	}
	if loadedPath != "" {
		t.Fatalf("loadedPath = %q, want empty", loadedPath)
	}
	if cfg.ListenAddr != "0.0.0.0" || cfg.ListenPort != 20440 || cfg.PrintHz != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Moza.Enabled || cfg.Moza.UpdateHz != 20 || cfg.Moza.RPMBrightness != 15 || cfg.Moza.ButtonMask != 0x03ff {
		t.Fatalf("unexpected moza defaults: %+v", cfg.Moza)
	}
	if cfg.Recording.Dir != "recordings" {
		t.Fatalf("unexpected recording defaults: %+v", cfg.Recording)
	}
	if cfg.Terminal.Enabled {
		t.Fatalf("unexpected terminal defaults: %+v", cfg.Terminal)
	}
	if cfg.Overlay.Enabled || cfg.Overlay.UpdateHz != 10 || cfg.Overlay.Opacity != 0.85 {
		t.Fatalf("unexpected overlay defaults: %+v", cfg.Overlay)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := os.WriteFile(path, []byte(`{
		"listen_addr":"127.0.0.1",
		"listen_port":20441,
		"print_hz":10,
		"recording":{"dir":"captures"},
		"terminal_print":{"enabled":true},
		"overlay":{
			"enabled":true,
			"output":"DP-1",
			"width":360,
			"height":160,
			"anchor":"top-right",
			"margin_top":24,
			"margin_right":24,
			"margin_bottom":0,
			"margin_left":0,
			"update_hz":20,
			"opacity":0.9
		},
		"moza":{
			"enabled":true,
			"port":"/dev/ttyACM1",
			"update_hz":30,
			"rpm_brightness":12,
			"rpm_colors":[[1,2,3]],
			"button_colors":[[4,5,6]],
			"button_mask":7
		}
	}`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1" || cfg.ListenPort != 20441 || cfg.PrintHz != 10 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if !cfg.Moza.Enabled || cfg.Moza.Port != "/dev/ttyACM1" || cfg.Moza.UpdateHz != 30 || cfg.Moza.RPMBrightness != 12 {
		t.Fatalf("unexpected moza config: %+v", cfg.Moza)
	}
	if cfg.Moza.RPMColors[0] != (Color{1, 2, 3}) || cfg.Moza.ButtonColors[0] != (Color{4, 5, 6}) || cfg.Moza.ButtonMask != 7 {
		t.Fatalf("unexpected moza color config: %+v", cfg.Moza)
	}
	if cfg.Overlay.Output != "DP-1" {
		t.Fatalf("unexpected overlay output: %+v", cfg.Overlay)
	}
	if cfg.Recording.Dir != "captures" {
		t.Fatalf("unexpected recording config: %+v", cfg.Recording)
	}
	if !cfg.Terminal.Enabled {
		t.Fatalf("unexpected terminal config: %+v", cfg.Terminal)
	}
	if !cfg.Overlay.Enabled || cfg.Overlay.Output != "DP-1" || cfg.Overlay.WidthValue() != 360 || cfg.Overlay.HeightValue() != 160 || cfg.Overlay.Anchor != "top-right" {
		t.Fatalf("unexpected overlay config: %+v", cfg.Overlay)
	}
}

func TestValidateRejectsReservedForzaPortRange(t *testing.T) {
	cfg := Default()
	cfg.ListenPort = 5200

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate returned nil, want error")
	}
}

func TestValidateAcceptsEnabledMozaWithoutPort(t *testing.T) {
	// An empty port is valid when MOZA is enabled: the runtime auto-detects the
	// attached wheel over USB, so the port need not be named.
	cfg := Default()
	cfg.Moza.Enabled = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned %v, want nil for empty port with auto-detect", err)
	}
}

func TestValidateRejectsOutOfRangeRPMLEDs(t *testing.T) {
	cfg := Default()
	cfg.Moza.Enabled = true
	cfg.Moza.RPMLEDs = 99

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate returned nil, want error for rpm_leds out of range")
	}
}

func TestValidateOverlayModeAcceptsCompleteOverlay(t *testing.T) {
	cfg := Default()
	cfg.Overlay = Overlay{
		Enabled:      true,
		Width:        intPtr(360),
		Height:       intPtr(160),
		Anchor:       "bottom-right",
		MarginTop:    intPtr(0),
		MarginRight:  intPtr(20),
		MarginBottom: intPtr(20),
		MarginLeft:   intPtr(0),
		UpdateHz:     15,
		Opacity:      0.8,
	}

	if err := cfg.ValidateOverlayMode(); err != nil {
		t.Fatalf("ValidateOverlayMode returned error: %v", err)
	}
}

func TestValidateOverlayModeRejectsDisabledOverlay(t *testing.T) {
	cfg := Default()

	if err := cfg.ValidateOverlayMode(); err == nil {
		t.Fatal("ValidateOverlayMode returned nil, want error")
	}
}

func TestValidateOverlayModeRejectsIncompletePlacement(t *testing.T) {
	cfg := Default()
	cfg.Overlay.Enabled = true
	cfg.Overlay.Width = intPtr(360)
	cfg.Overlay.Height = intPtr(160)
	cfg.Overlay.UpdateHz = 15
	cfg.Overlay.Opacity = 0.8
	// Default() now fills in placement defaults, so clear a margin to exercise
	// the incomplete-placement rejection.
	cfg.Overlay.MarginTop = nil

	if err := cfg.ValidateOverlayMode(); err == nil {
		t.Fatal("ValidateOverlayMode returned nil, want error")
	}
}

func TestValidateOverlayModeRejectsInvalidOpacity(t *testing.T) {
	cfg := Default()
	cfg.Overlay = Overlay{
		Enabled:      true,
		Width:        intPtr(360),
		Height:       intPtr(160),
		Anchor:       "top",
		MarginTop:    intPtr(0),
		MarginRight:  intPtr(0),
		MarginBottom: intPtr(0),
		MarginLeft:   intPtr(0),
		UpdateHz:     15,
		Opacity:      1.1,
	}

	if err := cfg.ValidateOverlayMode(); err == nil {
		t.Fatal("ValidateOverlayMode returned nil, want error")
	}
}

func TestSaveWritesIndentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Recording.Dir = "captures"

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if loaded.Recording.Dir != "captures" {
		t.Fatalf("loaded recording dir = %q", loaded.Recording.Dir)
	}
}

func TestSteeringPositionDefaultsToLegacyPlacement(t *testing.T) {
	// With SteeringX/Y unset, the helpers must reproduce the legacy hardcoded
	// top-right placement so existing configs render unchanged.
	o := Overlay{Width: intPtr(320), SteeringSize: intPtr(60)}
	if got, want := o.SteeringXValue(), 320-60-steeringGap; got != want {
		t.Fatalf("SteeringXValue() = %d, want %d", got, want)
	}
	if got := o.SteeringYValue(); got != 8 {
		t.Fatalf("SteeringYValue() = %d, want 8", got)
	}
}

func TestSteeringPositionHonorsExplicitValues(t *testing.T) {
	o := Overlay{Width: intPtr(320), SteeringSize: intPtr(60), SteeringX: intPtr(10), SteeringY: intPtr(20)}
	if got := o.SteeringXValue(); got != 10 {
		t.Fatalf("SteeringXValue() = %d, want 10", got)
	}
	if got := o.SteeringYValue(); got != 20 {
		t.Fatalf("SteeringYValue() = %d, want 20", got)
	}
}

func TestWithDefaultsFillsOpacity(t *testing.T) {
	o := Overlay{}.WithDefaults()
	if o.Opacity != defaultOpacity {
		t.Fatalf("WithDefaults() opacity = %v, want %v", o.Opacity, defaultOpacity)
	}
}
