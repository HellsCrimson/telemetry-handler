package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"telemetry-handler/app"
	"telemetry-handler/config"
	"telemetry-handler/recording"
	"telemetry-handler/store"
	"telemetry-handler/wheelbase/moza"
)

//go:embed all:frontend/dist
var assets embed.FS

// resolveTestProtocol turns the -moza-protocol flag into a concrete protocol for
// the standalone test/probe, auto-detecting the rim over serial when "auto".
func resolveTestProtocol(flagVal, port string) moza.Protocol {
	protocol, model := moza.ResolveWheel(moza.ParseProtocol(flagVal), port)
	name := "legacy"
	if protocol == moza.ProtocolNew {
		name = "new"
	}
	log.Printf("moza: using %s protocol for wheel %q", name, model)
	return protocol
}

func main() {
	configPath := flag.String("config", "", "path to JSON config file")
	mozaTest := flag.Bool("moza-test", false, "run an experimental MOZA wheel light test and exit")
	mozaPort := flag.String("moza-port", "", "MOZA serial device path for -moza-test/-moza-led-probe, for example /dev/ttyACM1")
	mozaDuration := flag.Duration("moza-test-duration", 10*time.Second, "duration for -moza-test")
	mozaLEDProbe := flag.Bool("moza-led-probe", false, "light each rev-light segment one at a time to identify the rim's LED layout, then exit")
	mozaLEDProbeHold := flag.Duration("moza-led-probe-hold", 600*time.Millisecond, "how long to hold each segment during -moza-led-probe")
	mozaProtocol := flag.String("moza-protocol", "auto", "rim LED protocol for -moza-test/-moza-led-probe: \"auto\" (detect), \"old\" (legacy rims), or \"new\" (ESX and other newer rims)")
	voiceSTT := flag.String("voice-stt", "", "transcribe a WAV file with the configured whisper.cpp, then parse + dry-run the pit plan, and exit")
	voiceListen := flag.Bool("voice-listen", false, "record from the mic, transcribe, and dry-run the pit plan (no trigger, applies nothing), then exit")
	voiceSay := flag.String("voice-say", "", "parse a phrase and print the pit-menu changes it would make against the live LMU menu (dry run), then exit")
	voiceMenu := flag.Bool("voice-menu", false, "dump the live LMU pit menu (component names + option labels) for tuning the voice matching, then exit")
	voiceDuration := flag.Duration("voice-duration", 4*time.Second, "recording duration for -voice-listen")
	flag.Parse()

	if *mozaTest {
		if *mozaPort == "" {
			log.Fatal("moza test requires -moza-port, for example -moza-port /dev/ttyACM1")
		}
		protocol := resolveTestProtocol(*mozaProtocol, *mozaPort)
		if err := moza.RunLightTest(*mozaPort, *mozaDuration, protocol); err != nil {
			log.Fatalf("moza test: %v", err)
		}
		return
	}

	if *mozaLEDProbe {
		if *mozaPort == "" {
			log.Fatal("moza led probe requires -moza-port, for example -moza-port /dev/ttyACM1")
		}
		protocol := resolveTestProtocol(*mozaProtocol, *mozaPort)
		// Probe the full addressable range so a rim with any segment count is fully
		// covered; the user counts which physical LEDs respond.
		if err := moza.RunLEDProbe(*mozaPort, 16, *mozaLEDProbeHold, protocol); err != nil {
			log.Fatalf("moza led probe: %v", err)
		}
		return
	}

	// A malformed/invalid config used to abort startup. Instead, fall back to
	// defaults and surface the error (native dialog + in-app banner) so the app
	// always starts and the user can see what went wrong.
	cfg, loadedPath, loadErr := config.LoadOptional(*configPath)
	var loadErrMsg, loadErrPath string
	if loadErr != nil {
		loadErrPath = *configPath
		if loadErrPath == "" {
			loadErrPath = "config.json"
		}
		loadErrMsg = loadErr.Error()
		log.Printf("config error (starting with defaults): %v", loadErr)
		cfg = config.Default()
		loadedPath = ""
	}
	if loadedPath == "" {
		log.Printf("using defaults: listen=%s:%d print_hz=%.2f", cfg.ListenAddr, cfg.ListenPort, cfg.PrintHz)
	} else {
		log.Printf("loaded config %s: listen=%s:%d print_hz=%.2f", loadedPath, cfg.ListenAddr, cfg.ListenPort, cfg.PrintHz)
	}

	// Voice bring-up harness: headless, exercises one stage and exits. Applies
	// nothing (the pit plan is a dry run) — see voicecli.go.
	if *voiceMenu {
		if err := dumpPitMenu(cfg); err != nil {
			log.Fatalf("voice menu: %v", err)
		}
		return
	}
	if *voiceSTT != "" || *voiceListen || *voiceSay != "" {
		if err := runVoiceTest(cfg, *voiceSTT, *voiceSay, *voiceListen, *voiceDuration); err != nil {
			log.Fatalf("voice test: %v", err)
		}
		return
	}

	recorder, err := recording.NewManager(cfg.Recording.Dir)
	if err != nil {
		log.Fatalf("recording: %v", err)
	}

	// Local SQLite store for data that outlives a run (reference laps, corner
	// names, session history, recordings index). A failure here is non-fatal: the
	// app runs without persistence, exactly as before.
	st, err := store.Open("telemetry.db")
	if err != nil {
		log.Printf("store: %v (continuing without persistence)", err)
		st = nil
	}

	runtime := app.NewRuntime(cfg, loadedPath, recorder, st)
	if loadErrMsg != "" {
		runtime.SetLoadError(loadErrPath, loadErrMsg)
	}
	service := app.NewService(runtime)

	wailsApp := application.New(application.Options{
		Name:        "telemetry-handler",
		Description: "Real-time Forza telemetry dashboard and overlay",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Telemetry Handler",
		Width:            1280,
		Height:           860,
		BackgroundColour: application.NewRGB(10, 13, 16),
		URL:              "/",
	})

	// If the config failed to load, pop a native error dialog once the app is up
	// (the window/event loop must be running before a dialog can be shown). Run
	// it on a goroutine so the synchronous Show() doesn't block the main thread.
	if loadErrMsg != "" {
		message := fmt.Sprintf(
			"Could not load %s:\n\n%s\n\nThe app started with default settings. Fix the file and restart, or adjust settings in the app and Save to overwrite it.",
			loadErrPath, loadErrMsg,
		)
		wailsApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			go wailsApp.Dialog.Error().SetTitle("Configuration error").SetMessage(message).Show()
		})
	}

	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}
