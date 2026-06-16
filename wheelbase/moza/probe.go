//go:build linux || windows

package moza

import (
	"fmt"
	"time"
)

// RunLEDProbe is a diagnostic for identifying a rim's rev-light layout, which
// USB cannot report (the descriptor only names the base). It lights each LED
// segment individually from index 0 up to maxLEDs-1 — holding each long enough
// to count — then fills the bar cumulatively, printing the segment it is
// driving. Watching which physical LEDs respond, and in what order, reveals the
// real segment count to set as moza.rpm_leds. It restores telemetry mode off on
// exit so it leaves the wheel as it found it.
func RunLEDProbe(path string, maxLEDs int, hold time.Duration, protocol Protocol) error {
	maxLEDs = clampRPMLEDs(maxLEDs)
	if hold <= 0 {
		hold = 600 * time.Millisecond
	}

	conn, err := openSerial(path)
	if err != nil {
		return err
	}
	defer conn.Close()

	if protocol == ProtocolNew {
		return runLEDProbeNew(conn, hold)
	}

	// Cycle a small palette so neighbouring segments are easy to tell apart.
	palette := []RGB{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}, {255, 0, 255}}
	var colors [10]RGB
	for i := range colors {
		colors[i] = palette[i%len(palette)]
	}
	frames, err := setupTelemetryFrames(15, colors, colors, 0)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := conn.WriteFrame(frame); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}

	fmt.Printf("MOZA LED probe on %s: lighting segments 0..%d one at a time\n", path, maxLEDs-1)
	for i := 0; i < maxLEDs; i++ {
		fmt.Printf("  segment %d (bit %d)\n", i, i)
		if err := writeMask(conn, uint16(1)<<uint(i)); err != nil {
			return err
		}
		time.Sleep(hold)
	}

	fmt.Println("filling cumulatively 1..N segments")
	for n := 1; n <= maxLEDs; n++ {
		fmt.Printf("  %d segment(s) lit\n", n)
		if err := writeMask(conn, uint16((1<<uint(n))-1)); err != nil {
			return err
		}
		time.Sleep(hold)
	}

	if err := writeMask(conn, 0); err != nil {
		return err
	}
	mode, err := setTelemetryMode(false)
	if err != nil {
		return err
	}
	return conn.WriteFrame(mode)
}
