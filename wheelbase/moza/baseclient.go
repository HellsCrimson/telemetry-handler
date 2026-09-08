package moza

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

const (
	// baseTimeout bounds one request/response exchange. The base answers in single
	// -digit milliseconds when it answers at all; this is generous enough to
	// survive a busy USB bus without stalling a settings page.
	baseTimeout = 400 * time.Millisecond
	// baseReadChunk is the read buffer size. Frames are a handful of bytes, but
	// the port may hand back several at once.
	baseReadChunk = 256
	// writeGap paces a grouped apply. See readGap in basesession.go for the
	// measurements behind it; a write is a request/response exchange like any
	// other and overruns the base the same way.
	writeGap = 5 * time.Millisecond
)

// ErrUnsupported marks a command the base did not answer. It is distinct from a
// transport failure: the port is fine, this base simply does not implement the
// setting, and the caller should mark it unavailable rather than fail the page.
var ErrUnsupported = errors.New("moza: command not supported by this base")

// port is the subset of the serial connection the client needs. Declaring it
// here keeps BaseClient testable against a scripted port with no hardware.
type port interface {
	WriteFrame(frame []byte) error
	read(p []byte) (int, error)
}

// BaseClient reads and writes the wheelbase's persistent settings.
//
// It is deliberately separate from Driver. Driver exists to push LED output at
// telemetry rate and is effectively write-only; these settings are stored on the
// hardware and need request/response with checksum validation and acknowledgement
// matching. Both talk to the same physical port, so a single client owns the
// exchange and callers serialise through it — LED streaming must not interleave
// with a settings transaction, or the replies land in the wrong reader.
type BaseClient struct {
	mu   sync.Mutex
	conn port
	// acc holds bytes read but not yet consumed, so a frame split across two
	// reads is reassembled rather than lost.
	acc []byte
	// timeout is per exchange; overridable for tests.
	timeout time.Duration
	// now is injectable so deadline behaviour is testable without sleeping.
	now func() time.Time
}

// NewBaseClient wraps an open connection.
func NewBaseClient(conn port) *BaseClient {
	return &BaseClient{conn: conn, timeout: baseTimeout, now: time.Now}
}

// ReadSetting reads one setting from the base.
func (c *BaseClient) ReadSetting(cmd BaseCommand) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	group, device, id := cmd.readGroup(), cmd.device(), cmd.readID()
	frame, err := buildFrame(group, device, id, nil)
	if err != nil {
		return 0, err
	}
	reply, err := c.exchange(frame, group, device, id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", cmd.Key, err)
	}
	value, err := cmd.decode(reply.payloadAfter(id))
	if err != nil {
		return 0, err
	}
	return value, nil
}

// WriteSetting writes one setting and verifies the acknowledgement.
//
// The value is validated against the command's envelope before it reaches the
// wire, so a value the UI should never have offered cannot reach the hardware.
func (c *BaseClient) WriteSetting(cmd BaseCommand, value int) error {
	payload, err := cmd.encode(value)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	group, device, id := cmd.writeGroup(), cmd.device(), cmd.writeID()
	frame, err := buildFrame(group, device, id, payload)
	if err != nil {
		return err
	}
	reply, err := c.exchange(frame, group, device, id)
	if err != nil {
		return fmt.Errorf("write %s: %w", cmd.Key, err)
	}
	// The ack usually echoes the written value. Treat a mismatch as a failure —
	// the caller needs to know the base did not take it, rather than believing a
	// change that never happened.
	if got, err := cmd.decode(reply.payloadAfter(id)); err == nil && got != value {
		return fmt.Errorf("write %s: base acknowledged %d, wrote %d", cmd.Key, got, value)
	}
	return nil
}

// SettingsPatch is a partial set of settings, keyed by command key. Partial is
// the normal case: a user may want the app to manage steering angle and FFB
// strength and leave everything else exactly as Pit House left it.
type SettingsPatch map[string]int

// ApplyResult reports what a grouped apply actually did. A partial apply is a
// real outcome, not an exception — the caller must be able to tell the user
// which settings are live and which are unknown.
type ApplyResult struct {
	Applied []string
	Failed  map[string]error
}

// Ok reports whether every requested setting was written.
func (r ApplyResult) Ok() bool { return len(r.Failed) == 0 }

// ApplySettings writes a patch, safety envelope first.
//
// The ordering is a safety property rather than tidiness. A grouped apply can be
// cut short by a crash, a USB drop or the user quitting, and these settings
// persist on the hardware. Writing the torque cap, angle limit and speed limit
// before the feel settings means an interrupted apply leaves the base more
// restricted than either the old or the new preset intended. The reverse order
// can pair a previous preset's high torque cap with limits that never arrived.
//
// It does not stop at the first failure: an unsupported command on an older base
// should not prevent the rest of a preset from landing. Everything that failed
// is reported.
func (c *BaseClient) ApplySettings(patch SettingsPatch) (ApplyResult, error) {
	// A safety limit's direction decides when it is written, so the current
	// values are needed before anything is ordered. Only the safety keys are
	// read — three at most, and only those present in the patch.
	raising, err := c.risingSafetyKeys(patch)
	if err != nil {
		return ApplyResult{}, err
	}
	keys, err := orderedKeys(patch, raising)
	if err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{Failed: map[string]error{}}
	for _, key := range keys {
		// Paced for the same reason bulk reads are: back-to-back requests overrun
		// the base and it stops answering some of them. A dropped read shows an
		// empty field; a dropped WRITE means a setting the user believes they
		// changed silently did not change, which is the worse of the two.
		time.Sleep(writeGap)
		cmd, _ := LookupBaseCommand(key)
		if err := c.WriteSetting(cmd, patch[key]); err != nil {
			result.Failed[key] = err
			continue
		}
		result.Applied = append(result.Applied, key)
	}
	return result, nil
}

// risingSafetyKeys reads the safety settings in a patch and reports which are
// being RAISED — that is, made more permissive than the wheel is now.
//
// A read failure here is not fatal: treating a limit as rising is the cautious
// assumption, since it pushes the write to the end where an interruption leaves
// the wheel restricted rather than loose.
func (c *BaseClient) risingSafetyKeys(patch SettingsPatch) (map[string]bool, error) {
	rising := map[string]bool{}
	for key, target := range patch {
		if !IsSafetyCommand(key) {
			continue
		}
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
		current, err := c.ReadSetting(cmd)
		if err != nil {
			rising[key] = true // cautious: write it last
			continue
		}
		rising[key] = target > current
	}
	return rising, nil
}

// orderedKeys sorts a patch into apply order.
//
// The invariant is that an apply cut short — by a crash, a USB drop, the user
// quitting — leaves the wheel MORE restricted than either the old or the new
// preset intended, never less. Delivering that takes three rules:
//
//  1. A safety limit being LOWERED is written first; one being RAISED is written
//     last. Direction matters and this is easy to get wrong: "safety first"
//     alone is only correct when limits are coming down. A preset that raises
//     the torque cap and is then interrupted would otherwise leave the higher
//     cap paired with the previous preset's settings — more permissive than
//     anything the user asked for, which is the exact failure the rule exists to
//     prevent.
//  2. A setting that rewrites others as a side effect goes before the settings
//     it rewrites. Road sensitivity is a macro on this firmware — changing it
//     also moves the equalizer bands — so a preset carrying both must apply the
//     macro first and let the explicit band values land on top. The other order
//     silently discards what the user actually asked for.
//  3. Otherwise the registry's own order, so the sequence is deterministic and
//     reviewable.
func orderedKeys(patch SettingsPatch, raising map[string]bool) ([]string, error) {
	rank := make(map[string]int, len(baseCommands))
	for i, cmd := range baseCommands {
		rank[cmd.Key] = i
	}
	keys := make([]string, 0, len(patch))
	for key := range patch {
		if _, ok := LookupBaseCommand(key); !ok {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
		keys = append(keys, key)
	}

	// rewrittenBy maps a key to a key in this patch that clobbers it.
	rewrittenBy := map[string]string{}
	for _, key := range keys {
		cmd, _ := LookupBaseCommand(key)
		for _, affected := range cmd.Affects {
			if _, alsoInPatch := patch[affected]; alsoInPatch {
				rewrittenBy[affected] = key
			}
		}
	}

	// tier puts tightening limits first, ordinary settings in the middle, and
	// loosening limits last.
	tier := func(key string) int {
		if !IsSafetyCommand(key) {
			return 1
		}
		if raising[key] {
			return 2
		}
		return 0
	}

	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if ta, tb := tier(a), tier(b); ta != tb {
			return ta < tb
		}
		// Whichever of the pair is clobbered by the other must come later.
		if rewrittenBy[b] == a {
			return true
		}
		if rewrittenBy[a] == b {
			return false
		}
		return rank[a] < rank[b]
	})
	return keys, nil
}

// Rewrites reports which settings in a patch will be overwritten by another
// setting's side effects. Applying still works — the ordering makes the explicit
// value win — but a caller may want to say so, since the user is asking for two
// things that interact.
func Rewrites(patch SettingsPatch) map[string]string {
	out := map[string]string{}
	for key := range patch {
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			continue
		}
		for _, affected := range cmd.Affects {
			if _, alsoInPatch := patch[affected]; alsoInPatch {
				out[affected] = key
			}
		}
	}
	return out
}

// exchange writes a request and waits for the matching reply.
//
// Frames that do not match — an unsolicited status frame, a reply to something
// else — are discarded rather than mistaken for the answer.
func (c *BaseClient) exchange(request []byte, group, device uint8, id []uint8) (Frame, error) {
	if err := c.conn.WriteFrame(request); err != nil {
		return Frame{}, err
	}

	deadline := c.now().Add(c.timeout)
	buf := make([]byte, baseReadChunk)
	for c.now().Before(deadline) {
		// Drain anything already accumulated before reading more, so a reply that
		// arrived alongside an earlier frame is not left sitting in the buffer.
		if frame, ok := c.takeMatching(group, device, id); ok {
			return frame, nil
		}

		n, err := c.conn.read(buf)
		if n > 0 {
			c.acc = append(c.acc, buf[:n]...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Frame{}, err
		}
	}
	if frame, ok := c.takeMatching(group, device, id); ok {
		return frame, nil
	}
	return Frame{}, ErrUnsupported
}

// takeMatching consumes frames from the accumulator until one matches, returning
// it. Non-matching frames are dropped; a trailing partial frame is kept.
func (c *BaseClient) takeMatching(group, device uint8, id []uint8) (Frame, bool) {
	for len(c.acc) > 0 {
		frame, consumed, err := scanFrame(c.acc)
		if errors.Is(err, errShortFrame) {
			// Keep the partial tail and wait for the rest.
			c.acc = c.acc[consumed:]
			return Frame{}, false
		}
		if err != nil {
			c.acc = nil // nothing usable in the buffer
			return Frame{}, false
		}
		c.acc = c.acc[consumed:]
		if frame.matches(group, device, id) {
			return frame, true
		}
	}
	return Frame{}, false
}
