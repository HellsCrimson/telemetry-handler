package main

import (
	"context"
	"fmt"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/game/lmu/rest"
	"telemetry-handler/voice"
)

// runVoiceTest is the headless bring-up harness for the voice feature (mirrors
// the -moza-test pattern). It exercises one stage at a time and NEVER applies a
// pit change — Resolve is a read-only dry run that just prints the pit-menu
// writes a phrase would make. The stages:
//
//   -voice-stt FILE   transcribe a WAV with the configured whisper.cpp, then
//                     parse + dry-run resolve the result.
//   -voice-listen     record from the mic for -voice-duration, then the same.
//   -voice-say TEXT   skip audio entirely and parse + dry-run resolve TEXT.
//
// The dry-run resolve needs LMU running in a session (the pit-menu REST endpoint
// is state-gated); when it is not reachable the parse is still printed and the
// menu read error is reported, so STT can be validated without the game.
func runVoiceTest(cfg config.Config, sttFile, sayText string, listen bool, dur time.Duration) error {
	ctx := context.Background()

	var text string
	switch {
	case sttFile != "":
		t, err := transcribe(ctx, cfg, sttFile)
		if err != nil {
			return err
		}
		text = t
	case listen:
		t, err := recordAndTranscribe(ctx, cfg, dur)
		if err != nil {
			return err
		}
		text = t
	default:
		text = sayText
	}

	fmt.Printf("heard: %q\n", text)
	dryRunPlan(ctx, cfg, text)
	return nil
}

// transcribe runs whisper.cpp on a WAV file using the configured binary/model.
func transcribe(ctx context.Context, cfg config.Config, wav string) (string, error) {
	tr := voice.WhisperTranscriber{
		Bin:   cfg.Voice.WhisperBin,
		Model: cfg.Voice.WhisperModel,
		Lang:  cfg.Voice.Language,
	}
	return tr.Transcribe(ctx, wav)
}

// recordAndTranscribe records from the mic for dur (using the configured
// recorder), then transcribes it — the full input chain minus the PTT trigger.
func recordAndTranscribe(ctx context.Context, cfg config.Config, dur time.Duration) (string, error) {
	cap := voice.ExecCapturer{CmdTemplate: cfg.Voice.CaptureCmd}
	stop := make(chan struct{})
	go func() {
		time.Sleep(dur)
		close(stop)
	}()
	fmt.Printf("recording for %s — speak now…\n", dur)
	wav, err := cap.Capture(ctx, stop)
	if err != nil {
		return "", err
	}
	defer cap.Cleanup(wav)
	return transcribe(ctx, cfg, wav)
}

// dumpPitMenu prints the live LMU pit menu — every component's name, PMC value,
// current selection and all option labels — so the keyword matching in
// voice/actions.go can be tuned to the game's actual strings.
func dumpPitMenu(cfg config.Config) error {
	client := rest.NewClient(cfg.LMU.BaseURL, 3*time.Second)
	items, err := client.PitMenu(context.Background())
	if err != nil {
		return fmt.Errorf("read pit menu (is LMU in a session/garage?): %w", err)
	}
	if len(items) == 0 {
		fmt.Println("pit menu is empty — are you in an active session?")
		return nil
	}
	for _, it := range items {
		fmt.Printf("%-28s pmc=%d current=%d\n", it.Name, it.PMCValue, it.CurrentSetting)
		for i, s := range it.Settings {
			marker := "  "
			if i == it.CurrentSetting {
				marker = "->"
			}
			fmt.Printf("    [%d]%s %q\n", i, marker, s)
		}
	}
	return nil
}

// dryRunPlan parses text and prints what it would do, resolving pit actions
// against the live LMU pit menu without applying anything.
func dryRunPlan(ctx context.Context, cfg config.Config, text string) {
	u := voice.Parse(text)
	switch {
	case len(u.Actions) > 0:
		fmt.Printf("parsed: %d action(s)\n", len(u.Actions))
	case u.Affirm:
		fmt.Println("parsed: AFFIRM (would confirm a pending change)")
		return
	case u.Cancel:
		fmt.Println("parsed: CANCEL (would drop a pending change)")
		return
	default:
		fmt.Println("parsed: nothing actionable")
		return
	}

	client := rest.NewClient(cfg.LMU.BaseURL, 3*time.Second)
	plan, err := voice.Resolve(ctx, client, u.Actions)
	if err != nil {
		fmt.Printf("  could not read live pit menu (is LMU in a session?): %v\n", err)
		return
	}
	fmt.Printf("plan: %s\n", plan.Desc)
	if len(plan.Writes) == 0 {
		fmt.Println("  (no pit-menu component matched — check the keyword consts in voice/actions.go against your live menu)")
		return
	}
	for _, w := range plan.Writes {
		fmt.Printf("  write: %-16s (pmc %d) -> [%d] %q\n", w.Name, w.PMC, w.Setting, w.Label)
	}
	fmt.Println("(dry run — nothing was applied)")
}
