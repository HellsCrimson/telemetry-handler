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
)

type Color [3]uint8

type Config struct {
	ListenAddr string  `json:"listen_addr"`
	ListenPort int     `json:"listen_port"`
	PrintHz    float64 `json:"print_hz"`
	Moza       Moza    `json:"moza"`
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
	return nil
}
