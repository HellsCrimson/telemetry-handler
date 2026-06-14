//go:build linux || windows

package moza

import (
	"fmt"
	"time"
)

// reopenBackoff rate-limits reconnect attempts so a truly absent device does
// not trigger an OpenSerial storm at the telemetry packet rate.
const reopenBackoff = time.Second

// writeMasksWithRetry writes the RPM/button masks and, on failure, attempts a
// single transparent reconnect before retrying once. A MOZA wheel on USB serial
// occasionally throws a transient EIO (brief stall / re-enumeration); reopening
// the port recovers the lighting without restarting the app. Callers already
// hold d.mu.
func (d *Driver) writeMasksWithRetry(rpmMask, buttonMask uint16) error {
	// d.conn is nil after a previous reopen failed (wheel unplugged). Skip the
	// write — which would panic on the nil connection — and go straight to a
	// reconnect attempt.
	if d.conn != nil {
		if err := d.writeMasks(rpmMask, buttonMask); err == nil {
			return nil
		}
	}
	if rerr := d.reopen(); rerr != nil {
		return fmt.Errorf("moza write failed (reconnect failed: %v)", rerr)
	}
	return d.writeMasks(rpmMask, buttonMask)
}

// reopen closes the current connection and opens a fresh one, re-running the
// telemetry setup so the device is back in its configured state. Attempts are
// throttled by reopenBackoff. Callers already hold d.mu.
func (d *Driver) reopen() error {
	now := time.Now()
	if !d.lastReopen.IsZero() && now.Sub(d.lastReopen) < reopenBackoff {
		return fmt.Errorf("reconnect throttled")
	}
	d.lastReopen = now

	if d.conn != nil {
		_ = d.conn.Close()
		d.conn = nil
	}

	conn, err := openSerial(d.port)
	if err != nil {
		return err
	}
	d.conn = conn

	if err := d.setup(d.setupOpts); err != nil {
		_ = conn.Close()
		d.conn = nil
		return err
	}
	// Force the next UpdateRPM to rewrite the mask onto the freshly configured
	// device.
	d.lastMask = ^uint16(0)
	return nil
}
