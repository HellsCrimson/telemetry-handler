package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const (
	defaultPath       = "config.json"
	defaultListenAddr = "0.0.0.0"
	defaultListenPort = 20440
	defaultPrintHz    = 5
	defaultWebAddr    = "127.0.0.1:8080"
	defaultRecordDir  = "recordings"
	defaultOverlayHz  = 10
	defaultOpacity    = 0.85
)

type Color [3]uint8

type Config struct {
	ListenAddr string    `json:"listen_addr"`
	ListenPort int       `json:"listen_port"`
	PrintHz    float64   `json:"print_hz"`
	Moza       Moza      `json:"moza"`
	Web        Web       `json:"web"`
	Recording  Recording `json:"recording"`
	Terminal   Terminal  `json:"terminal_print"`
	Overlay    Overlay   `json:"overlay"`
}

type Moza struct {
	Enabled       bool      `json:"enabled"`
	Port          string    `json:"port"`
	UpdateHz      float64   `json:"update_hz"`
	RPMBrightness uint8     `json:"rpm_brightness"`
	RPMColors     [10]Color `json:"rpm_colors"`
	ButtonColors  [10]Color `json:"button_colors"`
	ButtonMask    uint16    `json:"button_mask"`
}

type Web struct {
	Enabled bool   `json:"enabled"`
	Addr    string `json:"addr"`
}

type Recording struct {
	Dir string `json:"dir"`
}

type Terminal struct {
	Enabled bool `json:"enabled"`
}

type Overlay struct {
	Enabled      bool    `json:"enabled"`
	SourceURL    string  `json:"source_url,omitempty"`
	Output       string  `json:"output,omitempty"`
	Width        *int    `json:"width,omitempty"`
	Height       *int    `json:"height,omitempty"`
	Anchor       string  `json:"anchor"`
	MarginTop    *int    `json:"margin_top,omitempty"`
	MarginRight  *int    `json:"margin_right,omitempty"`
	MarginBottom *int    `json:"margin_bottom,omitempty"`
	MarginLeft   *int    `json:"margin_left,omitempty"`
	UpdateHz     float64 `json:"update_hz"`
	Opacity      float64 `json:"opacity"`
}

func Default() Config {
	return Config{
		ListenAddr: defaultListenAddr,
		ListenPort: defaultListenPort,
		PrintHz:    defaultPrintHz,
		Moza: Moza{
			Enabled:       false,
			Port:          "",
			UpdateHz:      20,
			RPMBrightness: 15,
			RPMColors: [10]Color{
				{0, 255, 0},
				{0, 255, 0},
				{0, 255, 0},
				{255, 255, 0},
				{255, 255, 0},
				{255, 128, 0},
				{255, 128, 0},
				{255, 0, 0},
				{255, 0, 0},
				{255, 0, 255},
			},
			ButtonColors: [10]Color{
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
			},
			ButtonMask: 0x03ff,
		},
		Web: Web{
			Enabled: true,
			Addr:    defaultWebAddr,
		},
		Recording: Recording{
			Dir: defaultRecordDir,
		},
		Terminal: Terminal{
			Enabled: false,
		},
		Overlay: Overlay{
			Enabled:  false,
			UpdateHz: defaultOverlayHz,
			Opacity:  defaultOpacity,
		},
	}
}

func LoadOptional(path string) (Config, string, error) {
	if path == "" {
		path = defaultPath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			cfg := Default()
			return cfg, "", cfg.Validate()
		} else if err != nil {
			return Config{}, "", err
		}
	}

	cfg, err := Load(path)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, path, nil
}

func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		path = defaultPath
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr must not be empty")
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("listen_port must be between 1 and 65535")
	}
	if c.ListenPort >= 5200 && c.ListenPort <= 5300 {
		return fmt.Errorf("listen_port must avoid Forza's reserved outgoing range 5200-5300")
	}
	if c.PrintHz <= 0 {
		return fmt.Errorf("print_hz must be greater than 0")
	}
	if c.Moza.Enabled {
		if c.Moza.Port == "" {
			return fmt.Errorf("moza.port must not be empty when moza.enabled is true")
		}
		if c.Moza.UpdateHz <= 0 {
			return fmt.Errorf("moza.update_hz must be greater than 0")
		}
		if c.Moza.RPMBrightness > 15 {
			return fmt.Errorf("moza.rpm_brightness must be between 0 and 15")
		}
		if c.Moza.ButtonMask > 0x03ff {
			return fmt.Errorf("moza.button_mask must fit the 10 button telemetry bits")
		}
	}
	if c.Web.Enabled && c.Web.Addr == "" {
		return fmt.Errorf("web.addr must not be empty when web.enabled is true")
	}
	if c.Recording.Dir == "" {
		return fmt.Errorf("recording.dir must not be empty")
	}
	return nil
}

func (c Config) ValidateOverlayMode() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Overlay.Enabled {
		return fmt.Errorf("overlay.enabled must be true when running -overlay")
	}
	if c.Overlay.Width == nil {
		return fmt.Errorf("overlay.width must be set when running -overlay")
	}
	if *c.Overlay.Width <= 0 {
		return fmt.Errorf("overlay.width must be greater than 0 when running -overlay")
	}
	if c.Overlay.Height == nil {
		return fmt.Errorf("overlay.height must be set when running -overlay")
	}
	if *c.Overlay.Height <= 0 {
		return fmt.Errorf("overlay.height must be greater than 0 when running -overlay")
	}
	if !validOverlayAnchor(c.Overlay.Anchor) {
		return fmt.Errorf("overlay.anchor must be one of top-left, top-right, bottom-left, bottom-right, top, bottom")
	}
	if c.Overlay.MarginTop == nil || c.Overlay.MarginRight == nil || c.Overlay.MarginBottom == nil || c.Overlay.MarginLeft == nil {
		return fmt.Errorf("overlay margins must all be set when running -overlay")
	}
	if *c.Overlay.MarginTop < 0 || *c.Overlay.MarginRight < 0 || *c.Overlay.MarginBottom < 0 || *c.Overlay.MarginLeft < 0 {
		return fmt.Errorf("overlay margins must be greater than or equal to 0")
	}
	if c.Overlay.UpdateHz <= 0 {
		return fmt.Errorf("overlay.update_hz must be greater than 0")
	}
	if c.Overlay.Opacity < 0 || c.Overlay.Opacity > 1 {
		return fmt.Errorf("overlay.opacity must be between 0 and 1")
	}
	return nil
}

func validOverlayAnchor(anchor string) bool {
	switch anchor {
	case "top-left", "top-right", "bottom-left", "bottom-right", "top", "bottom":
		return true
	default:
		return false
	}
}

func (o Overlay) WidthValue() int {
	if o.Width == nil {
		return 0
	}
	return *o.Width
}

func (o Overlay) HeightValue() int {
	if o.Height == nil {
		return 0
	}
	return *o.Height
}

func (o Overlay) MarginTopValue() int {
	if o.MarginTop == nil {
		return 0
	}
	return *o.MarginTop
}

func (o Overlay) MarginRightValue() int {
	if o.MarginRight == nil {
		return 0
	}
	return *o.MarginRight
}

func (o Overlay) MarginBottomValue() int {
	if o.MarginBottom == nil {
		return 0
	}
	return *o.MarginBottom
}

func (o Overlay) MarginLeftValue() int {
	if o.MarginLeft == nil {
		return 0
	}
	return *o.MarginLeft
}
