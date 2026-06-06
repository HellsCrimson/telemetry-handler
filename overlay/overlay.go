package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
)

const staleAfter = 2 * time.Second

type Backend interface {
	Run(context.Context, config.Overlay, <-chan HUD) error
}

type telemetrySnapshot struct {
	Telemetry  forza.Telemetry `json:"telemetry"`
	ReceivedAt time.Time       `json:"received_at"`
	Available  bool            `json:"available"`
}

func Run(ctx context.Context, cfg config.Config) error {
	if err := cfg.ValidateOverlayMode(); err != nil {
		return err
	}

	sourceURL := overlaySourceURL(cfg)
	client := &http.Client{Timeout: 3 * time.Second}
	first, err := fetchTelemetry(ctx, client, sourceURL)
	if err != nil {
		return fmt.Errorf("overlay telemetry source %s: %w", sourceURL, err)
	}

	updates := make(chan HUD, 1)
	updates <- FormatHUD(first.Telemetry, first.Available, first.ReceivedAt, time.Now())

	errc := make(chan error, 1)
	go func() {
		errc <- newBackend().Run(ctx, cfg.Overlay, updates)
	}()

	ticker := time.NewTicker(time.Duration(float64(time.Second) / cfg.Overlay.UpdateHz))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case now := <-ticker.C:
			snapshot, err := fetchTelemetry(ctx, client, sourceURL)
			if err != nil {
				snapshot = telemetrySnapshot{ReceivedAt: now, Available: false}
			}
			hud := FormatHUD(snapshot.Telemetry, snapshot.Available, snapshot.ReceivedAt, now)
			select {
			case updates <- hud:
			default:
				<-updates
				updates <- hud
			}
		}
	}
}

func overlaySourceURL(cfg config.Config) string {
	if cfg.Overlay.SourceURL != "" {
		return cfg.Overlay.SourceURL
	}
	addr := cfg.Web.Addr
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/") + "/api/telemetry"
	}
	return "http://" + addr + "/api/telemetry"
}

func fetchTelemetry(ctx context.Context, client *http.Client, url string) (telemetrySnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return telemetrySnapshot{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return telemetrySnapshot{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return telemetrySnapshot{}, fmt.Errorf("GET returned %s", resp.Status)
	}

	var snapshot telemetrySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return telemetrySnapshot{}, err
	}
	return snapshot, nil
}
