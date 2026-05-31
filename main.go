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
	"syscall"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/moza"
	"telemetry-handler/output"
	"telemetry-handler/receiver"
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

	var mozaDriver *moza.Driver
	if cfg.Moza.Enabled {
		var err error
		mozaDriver, err = moza.NewDriver(mozaOptionsFromConfig(cfg.Moza))
		if err != nil {
			log.Fatalf("moza: %v", err)
		}
		defer func() {
			if err := mozaDriver.Close(); err != nil {
				log.Printf("moza close: %v", err)
			}
		}()
		log.Printf("moza enabled: port=%s update_hz=%.2f", cfg.Moza.Port, cfg.Moza.UpdateHz)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	formatter := output.NewFormatter()
	printEvery := time.Duration(float64(time.Second) / cfg.PrintHz)
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

		if mozaDriver != nil {
			currentRPM := telemetry.CurrentEngineRpm
			if telemetry.IsRaceOn == 0 {
				currentRPM = 0
			}
			if err := mozaDriver.UpdateRPM(currentRPM, telemetry.EngineMaxRpm); err != nil {
				return err
			}
		}

		now := time.Now()
		if lastPrint.IsZero() || now.Sub(lastPrint) >= printEvery {
			lastPrint = now
			fmt.Println(formatter.Format(telemetry))
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("receiver: %v", err)
	}
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
