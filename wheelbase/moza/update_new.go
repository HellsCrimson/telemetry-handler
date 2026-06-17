//go:build linux || windows

package moza

import (
	"fmt"
	"time"
)

// updateRPMNew drives the new-protocol rim's rev lights from the current RPM by
// streaming the 32-bit lit-LED bitmask (group 0x41 / device 0x18 / cmd 0xfdde).
// The colours come from the palette loaded at setup; this only changes which
// LEDs are lit. Callers already hold d.mu. It throttles on the lit count (the
// only thing that changes the bar) plus updateMin, matching the legacy path.
func (d *Driver) updateRPMNew(currentRPM, maxRPM float32) error {
	mask := rpmMaskValue(currentRPM, maxRPM, d.rpmLEDs, d.curve)
	now := time.Now()
	if mask == d.lastMaskNew && now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}
	if now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}

	frame, err := setRPMMaskNew(mask)
	if err != nil {
		return err
	}
	if err := d.writeFramesWithRetry([][]byte{frame}); err != nil {
		return err
	}
	d.lastMaskNew = mask
	d.lastUpdate = now
	return nil
}

// TestLights runs a short rev-light sweep on the connected wheel so the user can
// confirm the LEDs work from the dashboard — the same effect as the -moza-test
// CLI, but through the driver's already-open connection. It works on either
// protocol, blocks for the ~3s sweep (holding d.mu so live RPM updates do not
// interleave), and leaves the bar off.
func (d *Driver) TestLights() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	const step = 110 * time.Millisecond
	for lit := 0; lit <= 10; lit++ {
		if err := d.testWrite(lit, false); err != nil {
			return err
		}
		time.Sleep(step)
	}
	if err := d.testWrite(10, true); err != nil { // redline flash hold
		return err
	}
	time.Sleep(500 * time.Millisecond)
	for lit := 10; lit >= 0; lit-- {
		if err := d.testWrite(lit, false); err != nil {
			return err
		}
		time.Sleep(step)
	}

	// Force the next live UpdateRPM to rewrite the real mask onto the wheel.
	d.lastMask = ^uint16(0)
	d.lastMaskNew = ^uint32(0)
	return d.testWrite(0, false)
}

// testWrite lights the first lit LEDs (or the redline flash) for TestLights,
// using whichever protocol the driver speaks. Callers already hold d.mu.
func (d *Driver) testWrite(lit int, redline bool) error {
	if d.protocol == ProtocolNew {
		mask := maskForLit(lit)
		if redline {
			mask = redlineMask
		}
		frame, err := setRPMMaskNew(mask)
		if err != nil {
			return err
		}
		return d.writeFrames([][]byte{frame})
	}

	var mask uint16
	if lit > 0 {
		mask = uint16(1)<<uint(min(lit, 16)) - 1
	}
	return d.writeMasks(mask, d.buttonMask)
}

// blankLEDs turns the rev lights off, using whichever protocol the driver
// speaks: the legacy path clears the masks and leaves telemetry mode, the new
// path sends an all-off rev-light mask. Callers already hold d.mu.
func (d *Driver) blankLEDs() error {
	if d.protocol == ProtocolNew {
		frame, err := setRPMMaskNew(0)
		if err != nil {
			return err
		}
		return d.writeFrames([][]byte{frame})
	}

	err := d.writeMasks(0, 0)
	mode, modeErr := setTelemetryMode(false)
	if modeErr == nil {
		modeErr = d.conn.WriteFrame(mode)
	}
	if err != nil {
		return err
	}
	return modeErr
}

// writeFrames writes a sequence of pre-built frames to the open connection.
func (d *Driver) writeFrames(frames [][]byte) error {
	for _, frame := range frames {
		if err := d.conn.WriteFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// writeFramesWithRetry mirrors writeMasksWithRetry for the new-protocol frames:
// it attempts the write, and on failure (or a nil connection from a prior failed
// reopen) transparently reconnects once before retrying. Callers already hold
// d.mu.
func (d *Driver) writeFramesWithRetry(frames [][]byte) error {
	if d.conn != nil {
		if err := d.writeFrames(frames); err == nil {
			return nil
		}
	}
	if rerr := d.reopen(); rerr != nil {
		return fmt.Errorf("moza write failed (reconnect failed: %v)", rerr)
	}
	return d.writeFrames(frames)
}

// writeNewSetup sends the channel-config burst (palette + mask off) to a bare
// connection, pacing the writes like Pit House does.
func writeNewSetup(conn *serialConn, colors [10]RGB) error {
	frames, err := setupTelemetryFramesNew(15, colors)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := conn.WriteFrame(frame); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}
	return nil
}

// runLEDProbeNew is the new-protocol counterpart to RunLEDProbe: it sends the
// channel-config burst (loading a gradient palette), then lights each of the
// rim's 10 LEDs one at a time via the rev-light mask and fills the bar
// cumulatively, so the user can count which physical LEDs respond. The mask is
// 10 LEDs wide here regardless of the requested max.
func runLEDProbeNew(conn *serialConn, hold time.Duration) error {
	const leds = 10
	if hold <= 0 {
		hold = 600 * time.Millisecond
	}

	if err := writeNewSetup(conn, defaultRPMGradient()); err != nil {
		return err
	}

	writeMask := func(mask uint32) error {
		frame, err := setRPMMaskNew(mask)
		if err != nil {
			return err
		}
		return conn.WriteFrame(frame)
	}

	fmt.Printf("MOZA LED probe (new protocol) on new-device 0x18: lighting LEDs 0..%d one at a time\n", leds-1)
	for i := range leds {
		fmt.Printf("  LED %d\n", i)
		if err := writeMask(uint32(1) << uint(i)); err != nil {
			return err
		}
		time.Sleep(hold)
	}

	fmt.Println("filling cumulatively 1..N LEDs")
	for n := 1; n <= leds; n++ {
		fmt.Printf("  %d LED(s) lit\n", n)
		if err := writeMask(maskForLit(n)); err != nil {
			return err
		}
		time.Sleep(hold)
	}

	return writeMask(0)
}

// runLightTestNew exercises the new-protocol rim: it sends the channel-config
// burst (loading the gradient palette), then sweeps the rev bar up and down via
// the lit-LED mask so the user can confirm the LEDs respond, and blanks the bar
// on exit.
func runLightTestNew(conn *serialConn, colors [10]RGB, duration time.Duration) error {
	if err := writeNewSetup(conn, colors); err != nil {
		return err
	}

	write := func(lit int) error {
		mask := maskForLit(lit)
		if lit >= 10 {
			// Demo the redline flash at the top of the sweep.
			mask = redlineMask
		}
		frame, err := setRPMMaskNew(mask)
		if err != nil {
			return err
		}
		return conn.WriteFrame(frame)
	}

	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(duration)

	lit, dir := 0, 1
	for {
		select {
		case <-deadline:
			return write(0)
		case <-ticker.C:
			if err := write(lit); err != nil {
				return err
			}
			lit += dir
			if lit >= 10 {
				lit, dir = 10, -1
			} else if lit <= 0 {
				lit, dir = 0, 1
			}
		}
	}
}
