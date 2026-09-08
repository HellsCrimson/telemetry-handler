package moza

import (
	"errors"
	"fmt"
	"time"
)

// Getting a port for a settings transaction.
//
// Two things want the same serial device: the LED driver, which streams rev-light
// masks at telemetry rate, and the settings client, which does request/response
// exchanges. They must not interleave — a reply landing while the LED path is
// mid-write is read by nobody, and an LED frame written between a settings
// request and its answer delays or corrupts the exchange.
//
// Rather than opening the device twice (which on Linux is permitted and produces
// exactly that race), settings transactions borrow whichever handle already
// exists:
//
//   - if the LED driver is running, they run under the driver's own mutex on the
//     driver's connection;
//   - if it is not, they open a short-lived connection of their own.
//
// The caller does not choose. WithBase picks, so there is no path where a
// second handle is opened behind the driver's back.

// WithBase runs fn against the wheelbase settings client, holding the driver's
// lock for the duration so no LED write can interleave.
//
// The driver may be nil (LED output disabled or the wheel not connected at
// startup), in which case port is opened for the call and closed after. That is
// the normal case for a settings page opened while telemetry is not running.
func WithBase(d *Driver, port string, fn func(*BaseClient) error) error {
	if d != nil {
		return d.withBase(fn)
	}
	if port == "" {
		return fmt.Errorf("moza: no serial port configured")
	}
	conn, err := openSerial(port)
	if err != nil {
		return fmt.Errorf("moza: open %s: %w", port, err)
	}
	defer conn.Close()
	return fn(NewBaseClient(conn))
}

// withBase runs fn on the driver's connection under the driver's lock.
//
// It deliberately does not attempt a reopen on failure. reconnect.go owns that,
// and it re-runs LED setup as part of it; a settings transaction that was in
// flight when the port dropped must fail loudly rather than silently resume,
// because the app cannot know whether the write landed. The caller re-reads from
// the device instead of assuming its staged values are live.
func (d *Driver) withBase(fn func(*BaseClient) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return fmt.Errorf("moza: not connected")
	}
	return fn(NewBaseClient(d.conn))
}

// readRetries is how many times a silent command is asked before the read gives
// up on it.
//
// Measured on a real R12 V2: the damping level answered on 2 of 3 single
// attempts. It is not a missing feature, it is an unreliable reply — so one
// attempt misreports a control the wheel plainly has, and even two was not
// enough to make a full read consistent. Three makes a spurious miss unlikely
// while costing nothing on the common path, since a command that answers does
// so on the first try.
const readRetries = 3

// readGap paces a bulk read. DO NOT REMOVE — it looks like a pointless sleep and
// is not.
//
// Without it the app fires every request back to back with no delay, which
// overruns the base: replies are intermittently never sent. Measured on an
// R12 V2, reading the whole registry:
//
//	no pacing, 1 attempt per command   damping level answered ~2 times in 3
//	no pacing, 3 attempts per command  still failed ~1 run in 2
//	5ms between requests               6 of 6 runs clean, 0 unanswered commands
//
// The retries above could not fix it because the failures are correlated rather
// than independent — hammering the device is what causes them, so asking again
// immediately just hammers it more. Pacing is the fix; the retries are now only
// a backstop for a genuinely dropped reply.
//
// 5ms across ~35 commands costs under 200ms on a read the user triggers by hand.
const readGap = 5 * time.Millisecond

// readMaybeUnsupported reads a command, retrying a silent one before concluding
// the base does not implement it. Transport failures are returned immediately —
// retrying a dead port just multiplies the wait.
func (c *BaseClient) readMaybeUnsupported(cmd BaseCommand) (value int, supported bool, err error) {
	for attempt := range readRetries {
		if attempt > 0 {
			time.Sleep(readGap)
		}
		value, err = c.ReadSetting(cmd)
		if err == nil {
			return value, true, nil
		}
		if !isUnsupported(err) {
			return 0, false, err
		}
		_ = attempt
	}
	return 0, false, nil
}

// ReadAllSettings reads every command in the registry.
//
// Unsupported commands are recorded rather than failing the read: an older base
// answering only part of the set should still produce a usable page, with the
// rest marked unavailable. A transport failure is different and aborts, because
// continuing would just produce a page full of spurious "unsupported".
func (c *BaseClient) ReadAllSettings() (SettingsSnapshot, error) {
	snap := SettingsSnapshot{
		Values:      map[string]int{},
		Unsupported: map[string]bool{},
	}
	for _, cmd := range BaseCommands() {
		time.Sleep(readGap)
		value, supported, err := c.readMaybeUnsupported(cmd)
		if err != nil {
			return snap, err
		}
		if !supported {
			snap.Unsupported[cmd.Key] = true
			continue
		}
		snap.Values[cmd.Key] = value
	}
	return snap, nil
}

// SettingsSnapshot is what the base currently holds.
type SettingsSnapshot struct {
	// Values holds every setting the base answered, keyed by command key.
	Values map[string]int
	// Unsupported names the commands this base did not answer after several
	// attempts.
	//
	// "Did not answer" is all it means. Some commands are genuinely absent on
	// older firmware, but at least one on this hardware simply replies
	// unreliably, so the app must not tell the user their wheel lacks a feature
	// on the strength of silence alone.
	Unsupported map[string]bool
}

// Read-only status fields in group 0x2b.
//
// Ids confirmed against Boxflat's command database and then against the wheel
// itself on 2026-09-08. An earlier guess of 0x02/0x03/0x04 read state-err, an
// unassigned id, and the MCU temperature respectively — which is how the page
// came to show "0, 0, 3600".
const (
	statusStateID     = 0x01
	statusErrorID     = 0x02
	statusMCUTempID   = 0x04
	statusMOSFETID    = 0x05
	statusMotorTempID = 0x06
)

// tempScale converts the raw temperature reading to degrees Celsius.
//
// The base reports hundredths of a degree: an idle R12 V2 answered 3600 for the
// MCU, which is 36.00 C. Boxflat's command database records no scale — it
// applies one in its own UI — so this is derived from the hardware rather than
// from the protocol notes, and is the reason readings are float64 from here on.
const tempScale = 100.0

var statusTemps = []struct {
	Key string
	ID  uint8
}{
	{Key: "mcu_temp", ID: statusMCUTempID},
	{Key: "mosfet_temp", ID: statusMOSFETID},
	{Key: "motor_temp", ID: statusMotorTempID},
}

// BaseStatus is the read-only device state.
type BaseStatus struct {
	// Temps holds whatever temperature fields answered, in degrees Celsius.
	Temps map[string]float64
	// State and Error are the base's own state words. Meaning of the values is
	// not yet decoded; they are surfaced raw because a non-zero error is worth
	// showing even before we can name it.
	State, Error       int
	HasState, HasError bool
	// Unsupported names status fields this base did not answer.
	Unsupported map[string]bool
}

// ReadStatus reads the read-only status group.
//
// Every field is optional: a base that answers none of them is not an error, it
// is a base whose status ids we have not confirmed for that model. The caller
// shows what came back.
func (c *BaseClient) ReadStatus() (BaseStatus, error) {
	status := BaseStatus{Temps: map[string]float64{}, Unsupported: map[string]bool{}}

	readRaw := func(key string, id uint8) (int, bool, error) {
		// Width and range are fixed for the status group; the settings registry's
		// envelopes do not apply here.
		cmd := BaseCommand{Key: key, ID: []uint8{id}, Bytes: 2, Min: 0, Max: 1<<16 - 1, ReadGroup: baseStatusGroup}
		value, supported, err := c.readMaybeUnsupported(cmd)
		if err != nil {
			return 0, false, err
		}
		if !supported {
			status.Unsupported[key] = true
			return 0, false, nil
		}
		return value, true, nil
	}

	if v, ok, err := readRaw("state", statusStateID); err != nil {
		return status, err
	} else if ok {
		status.State, status.HasState = v, true
	}
	if v, ok, err := readRaw("state_err", statusErrorID); err != nil {
		return status, err
	} else if ok {
		status.Error, status.HasError = v, true
	}
	for _, field := range statusTemps {
		v, ok, err := readRaw(field.Key, field.ID)
		if err != nil {
			return status, err
		}
		if ok {
			status.Temps[field.Key] = float64(v) / tempScale
		}
	}
	return status, nil
}

// isUnsupported reports whether err means "this base did not answer", as opposed
// to a transport failure.
func isUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }
