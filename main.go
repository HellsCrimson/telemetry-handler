package main

import (
	"embed"
	"flag"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"telemetry-handler/app"
	"telemetry-handler/config"
	"telemetry-handler/moza"
	"telemetry-handler/recording"
)

//go:embed all:frontend/dist
var assets embed.FS

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

	runtime := app.NewRuntime(cfg, loadedPath, recorder)
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

	if err := wailsApp.Run(); err != nil {
		log.Fatal(err)
	}
}
