//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/moza"
	"telemetry-handler/output"
	"telemetry-handler/receiver"
	"telemetry-handler/recording"
	"telemetry-handler/webui"
)

func main() {
	configPath := flag.String("config", "", "path to JSON config file")
	mozaTest := flag.Bool("moza-test", false, "run an experimental MOZA wheel light test and exit")
	mozaPort := flag.String("moza-port", "", "MOZA serial device path for -moza-test, for example /dev/ttyACM1")
	mozaDuration := flag.Duration("moza-test-duration", 10*time.Second, "duration for -moza-test")
	flag.Parse()

	if *mozaTest {
		if *mozaPort == "" {
			log.Fatal("moza test requires -moza-port, for example -moza-port /dev/ttyACM1")
		}
		if err := moza.RunLightTest(*mozaPort, *mozaDuration); err != nil {
			log.Fatalf("moza test: %v", err)
		}
		return
	}

	cfg, loadedPath, err := config.LoadOptional(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if loadedPath == "" {
		log.Printf("using defaults: listen=%s:%d print_hz=%.2f", cfg.ListenAddr, cfg.ListenPort, cfg.PrintHz)
	} else {
		log.Printf("loaded config %s: listen=%s:%d print_hz=%.2f", loadedPath, cfg.ListenAddr, cfg.ListenPort, cfg.PrintHz)
	}

	recorder, err := recording.NewManager(cfg.Recording.Dir)
	if err != nil {
		log.Fatalf("recording: %v", err)
	}

	runtime := newRuntime(cfg, loadedPath, recorder)
	defer runtime.Close()

	if cfg.Moza.Enabled {
		if err := runtime.applyMoza(cfg.Moza, nil); err != nil {
			log.Fatalf("moza: %v", err)
		}
		log.Printf("moza enabled: port=%s update_hz=%.2f", cfg.Moza.Port, cfg.Moza.UpdateHz)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Web.Enabled {
		server := webui.NewServer(runtime)
		go func() {
			log.Printf("web ui listening on http://%s", cfg.Web.Addr)
			if err := server.ListenAndServe(ctx, cfg.Web.Addr); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("web ui: %v", err)
				stop()
			}
		}()
	}

	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	formatter := output.NewFormatter()
	var lastPrint time.Time
	var badSizes uint64

	err = receiver.Listen(ctx, addr, func(_ context.Context, packet []byte) error {
		telemetry, err := forza.ParseFH6Packet(packet)
		if err != nil {
			var sizeErr *forza.PacketSizeError
			if errors.As(err, &sizeErr) {
				badSizes++
				log.Printf("ignored packet with unexpected size got=%d want=%d total_bad=%d", sizeErr.Got, sizeErr.Want, badSizes)
				return nil
			}
			return err
		}

		if err := runtime.RecordPacket(packet, time.Now()); err != nil {
			return err
		}

		runtime.SetTelemetry(telemetry)
		if runtime.MozaEnabled() {
			currentRPM := telemetry.CurrentEngineRpm
			if telemetry.IsRaceOn == 0 {
				currentRPM = 0
			}
			if err := runtime.UpdateMozaRPM(currentRPM, telemetry.EngineMaxRpm); err != nil {
				return err
			}
		}

		now := time.Now()
		if lastPrint.IsZero() || now.Sub(lastPrint) >= runtime.PrintEvery() {
			lastPrint = now
			fmt.Println(formatter.Format(telemetry))
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("receiver: %v", err)
	}
}

type appRuntime struct {
	mu        sync.RWMutex
	cfg       config.Config
	cfgPath   string
	telemetry forza.Telemetry
	seen      bool
	seenAt    time.Time
	moza      *moza.Driver
	recorder  *recording.Manager
}

func newRuntime(cfg config.Config, cfgPath string, recorder *recording.Manager) *appRuntime {
	return &appRuntime{cfg: cfg, cfgPath: cfgPath, recorder: recorder}
}

func (r *appRuntime) Config() config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *appRuntime) LatestTelemetry() webui.TelemetrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return webui.TelemetrySnapshot{
		Telemetry:  r.telemetry,
		ReceivedAt: r.seenAt,
		Available:  r.seen,
	}
}

func (r *appRuntime) SetTelemetry(telemetry forza.Telemetry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetry = telemetry
	r.seen = true
	r.seenAt = time.Now()
}

func (r *appRuntime) PrintEvery() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Duration(float64(time.Second) / r.cfg.PrintHz)
}

func (r *appRuntime) MozaEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.moza != nil
}

func (r *appRuntime) UpdateMozaRPM(currentRPM, maxRPM float32) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver := r.moza
	if driver == nil {
		return nil
	}
	return driver.UpdateRPM(currentRPM, maxRPM)
}

func (r *appRuntime) ApplyConfig(next config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if next.ListenAddr != r.cfg.ListenAddr || next.ListenPort != r.cfg.ListenPort {
		return fmt.Errorf("listen_addr and listen_port changes require restarting the process")
	}
	if next.Web.Addr != r.cfg.Web.Addr || next.Web.Enabled != r.cfg.Web.Enabled {
		return fmt.Errorf("web.enabled and web.addr changes require restarting the process")
	}
	if next.Recording.Dir != r.cfg.Recording.Dir {
		return fmt.Errorf("recording.dir changes require restarting the process")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := r.applyMoza(next.Moza, &next); err != nil {
		return err
	}
	r.cfg = next
	return nil
}

func (r *appRuntime) SaveConfig(next config.Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	return config.Save(r.cfgPath, next)
}

func (r *appRuntime) PreviewMoza(next config.Moza) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver := r.moza
	if driver == nil {
		return fmt.Errorf("MOZA preview requires an active MOZA driver")
	}
	if next.UpdateHz <= 0 {
		next.UpdateHz = 20
	}
	return driver.Apply(mozaOptionsFromConfig(next))
}

func (r *appRuntime) RecordPacket(packet []byte, at time.Time) error {
	if r.recorder == nil {
		return nil
	}
	return r.recorder.Record(packet, at)
}

func (r *appRuntime) StartRecording() (recording.Status, error) {
	if r.recorder == nil {
		return recording.Status{}, fmt.Errorf("recording is not available")
	}
	return r.recorder.Start()
}

func (r *appRuntime) StopRecording() (recording.Status, error) {
	if r.recorder == nil {
		return recording.Status{}, nil
	}
	return r.recorder.Stop()
}

func (r *appRuntime) RecordingStatus() recording.Status {
	if r.recorder == nil {
		return recording.Status{}
	}
	return r.recorder.Status()
}

func (r *appRuntime) ListRecordings() ([]recording.Info, error) {
	if r.recorder == nil {
		return nil, fmt.Errorf("recording is not available")
	}
	return r.recorder.List()
}

func (r *appRuntime) ReplayRecording(name string, maxSamples int) ([]webui.ReplaySample, error) {
	if r.recorder == nil {
		return nil, fmt.Errorf("recording is not available")
	}

	rawSamples, err := r.recorder.Read(name, maxSamples)
	if err != nil {
		return nil, err
	}

	samples := make([]webui.ReplaySample, 0, len(rawSamples))
	for _, raw := range rawSamples {
		telemetry, err := forza.ParseFH6Packet(raw.Packet)
		if err != nil {
			return nil, err
		}
		samples = append(samples, webui.ReplaySample{
			OffsetMS:  raw.OffsetMS,
			Telemetry: telemetry,
		})
	}
	return samples, nil
}

func (r *appRuntime) Close() {
	if r.recorder != nil {
		if _, err := r.recorder.Stop(); err != nil {
			log.Printf("recording stop: %v", err)
		}
	}

	r.mu.Lock()
	driver := r.moza
	r.moza = nil
	r.mu.Unlock()
	if driver != nil {
		if err := driver.Close(); err != nil {
			log.Printf("moza close: %v", err)
		}
	}
}

func (r *appRuntime) applyMoza(next config.Moza, staged *config.Config) error {
	if !next.Enabled {
		if r.moza != nil {
			if err := r.moza.Close(); err != nil {
				return err
			}
			r.moza = nil
		}
		return nil
	}

	if staged != nil {
		staged.Moza = next
		if err := staged.Validate(); err != nil {
			return err
		}
	}

	options := mozaOptionsFromConfig(next)
	currentPort := r.cfg.Moza.Port
	if r.moza != nil && currentPort == next.Port {
		return r.moza.Apply(options)
	}
	if r.moza != nil {
		if err := r.moza.Close(); err != nil {
			return err
		}
		r.moza = nil
	}
	driver, err := moza.NewDriver(options)
	if err != nil {
		return err
	}
	r.moza = driver
	return nil
}

func mozaOptionsFromConfig(cfg config.Moza) moza.Options {
	return moza.Options{
		Port:          cfg.Port,
		UpdateHz:      cfg.UpdateHz,
		RPMBrightness: cfg.RPMBrightness,
		RPMColors:     mozaColorsFromConfig(cfg.RPMColors),
		ButtonColors:  mozaColorsFromConfig(cfg.ButtonColors),
		ButtonMask:    cfg.ButtonMask,
	}
}

func mozaColorsFromConfig(colors [10]config.Color) [10]moza.RGB {
	var out [10]moza.RGB
	for i, color := range colors {
		out[i] = moza.RGB{R: color[0], G: color[1], B: color[2]}
	}
	return out
}
