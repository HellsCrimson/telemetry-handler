package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"telemetry-handler/assistant"
	"telemetry-handler/config"
	"telemetry-handler/game/lmu/rest"
	"telemetry-handler/voice"
)

// runVoiceTest is the headless bring-up harness for the voice feature (mirrors
// the -moza-test pattern). It exercises one stage at a time and NEVER applies a
// pit change — Resolve is a read-only dry run that just prints the pit-menu
// writes a phrase would make. The stages:
//
//	-voice-listen     record from the mic for -voice-duration, stream it to the
//	                  voice server, then parse + dry-run resolve the transcript.
//	-voice-say TEXT   skip audio entirely and parse + dry-run resolve TEXT.
//	-voice-speak TEXT synthesize TEXT on the voice server and play it, to check
//	                  the spoken-output half on its own.
//
// The dry-run resolve needs LMU running in a session (the pit-menu REST endpoint
// is state-gated); when it is not reachable the parse is still printed and the
// menu read error is reported, so STT can be validated without the game.
func runVoiceTest(cfg config.Config, sayText string, listen bool, dur time.Duration) error {
	ctx := context.Background()

	text := sayText
	if listen {
		t, err := recordAndTranscribe(ctx, cfg, dur)
		if err != nil {
			return err
		}
		text = t
	}

	fmt.Printf("heard: %q\n", text)
	dryRunPlan(ctx, cfg, text)
	return nil
}

// recordAndTranscribe records from the mic for dur and streams it to the voice
// server — the full input chain minus the push-to-talk trigger.
func recordAndTranscribe(ctx context.Context, cfg config.Config, dur time.Duration) (string, error) {
	listener, err := voice.NewRemoteListener(voiceRemoteConfig(cfg), cfg.Voice.CaptureCmd, cfg.Voice.Language, log.Printf)
	if err != nil {
		return "", err
	}
	stop := make(chan struct{})
	go func() {
		time.Sleep(dur)
		close(stop)
	}()
	fmt.Printf("recording for %s — speak now…\n", dur)
	started := time.Now()
	text, err := listener.Listen(ctx, stop)
	if err != nil {
		return "", err
	}
	fmt.Printf("(transcribed %s after the release)\n", time.Since(started)-dur)
	return text, nil
}

// runVoiceSpeak synthesizes a phrase on the voice server and plays it, so the
// spoken-output path can be checked without the mic or the game.
func runVoiceSpeak(cfg config.Config, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	timing, err := voice.SpeakOnce(ctx, voice.TTSConfig{
		Remote:    voiceRemoteConfig(cfg),
		Voice:     cfg.Voice.TTS.Voice,
		Speed:     cfg.Voice.TTS.Speed,
		PlayerCmd: cfg.Voice.TTS.PlayerCmd,
	}, text)
	if err != nil {
		return err
	}
	// Only the first figure is latency; the second is dominated by how long the
	// phrase takes to say, so it grows with the message and is not comparable.
	fmt.Printf("audio started in %s (finished speaking after %s)\n",
		timing.FirstAudio.Round(time.Millisecond), timing.Total.Round(time.Millisecond))
	return nil
}

// runEngineerAsk puts one question to the LLM race engineer with the live
// session as context and prints (and speaks) the answer — the bring-up path for
// the engineer, usable without the push-to-talk trigger.
func runEngineerAsk(cfg config.Config, question string) error {
	if !cfg.Voice.Engineer.Enabled {
		return fmt.Errorf("voice.engineer.enabled is false — turn it on in config.json or the dashboard")
	}
	e := cfg.Voice.Engineer
	llm := assistant.NewLLM(assistant.LLMConfig{
		BaseURL:     e.BaseURL,
		APIKey:      e.APIKeyValue(),
		Model:       e.Model,
		Timeout:     time.Duration(e.TimeoutSeconds * float64(time.Second)),
		MaxTokens:   e.MaxTokens,
		Temperature: e.Temperature,
	})
	if e.APIKeyValue() == "" {
		fmt.Printf("warning: no API key (set %s or voice.engineer.api_key)\n", config.EngineerAPIKeyEnv)
	}

	// A dry run has no live telemetry, so the briefing is empty unless the app is
	// also running — the point here is to exercise the endpoint and the prompt.
	in := assistant.NewInterpreter(llm, nil, log.Printf)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	started := time.Now()
	var first time.Duration
	reply, err := in.Interpret(ctx, question, func(sentence string) {
		if first == 0 {
			first = time.Since(started)
		}
		fmt.Printf("  %s\n", sentence)
	})
	if err != nil {
		return err
	}
	if len(reply.Actions) > 0 {
		fmt.Printf("engineer staged %d pit action(s) — dry run, nothing applied\n", len(reply.Actions))
		dryRunPlan(ctx, cfg, question)
		return nil
	}
	fmt.Printf("(first sentence in %s, full answer in %s)\n",
		first.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	return nil
}

func voiceRemoteConfig(cfg config.Config) voice.RemoteConfig {
	return voice.RemoteConfig{BaseURL: cfg.Voice.ServerURL, Token: cfg.Voice.ServerToken}
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
