//go:build linux

package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Linux evdev constants and the input_event wire layout. On 64-bit Linux the
// struct is: struct timeval{long sec; long usec} (16) + __u16 type + __u16 code +
// __s32 value = 24 bytes, no padding (value is naturally 4-aligned at offset 20).
const (
	evKey          = 0x01 // EV_KEY: key/button press events
	inputEventSize = 24
	keyValueUp     = 0
	keyValueDown   = 1
)

func decodeEvent(buf []byte) (typ, code uint16, value int32) {
	typ = binary.LittleEndian.Uint16(buf[16:18])
	code = binary.LittleEndian.Uint16(buf[18:20])
	value = int32(binary.LittleEndian.Uint32(buf[20:24]))
	return
}

// ButtonTrigger reads a single configured evdev button (e.g. a wheel-rim button)
// directly from /dev/input/eventX and emits press/release events. The button code
// is the evdev code learned via LearnButton. Reading evdev needs read access to
// the device (the input group / a udev rule), independent of window focus.
type ButtonTrigger struct {
	events chan Event
	f      *os.File
}

func newButtonTrigger(ctx context.Context, device string, code int) (*ButtonTrigger, error) {
	if device == "" {
		return nil, fmt.Errorf("voice: button device required (run LearnVoiceButton)")
	}
	f, err := os.Open(device)
	if err != nil {
		return nil, fmt.Errorf("voice: open %s: %w", device, err)
	}
	t := &ButtonTrigger{events: make(chan Event, 8), f: f}
	go t.loop(ctx, uint16(code))
	// Closing the fd on shutdown unblocks the blocking ReadFull in loop.
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	return t, nil
}

func (t *ButtonTrigger) Events() <-chan Event { return t.events }

func (t *ButtonTrigger) loop(ctx context.Context, code uint16) {
	defer close(t.events)
	buf := make([]byte, inputEventSize)
	for {
		if _, err := io.ReadFull(t.f, buf); err != nil {
			return // fd closed (shutdown) or read error
		}
		typ, c, val := decodeEvent(buf)
		if typ != evKey || c != code {
			continue
		}
		var ev Event
		switch val {
		case keyValueDown:
			ev = EventPress
		case keyValueUp:
			ev = EventRelease
		default:
			continue // auto-repeat (value 2) and others
		}
		select {
		case t.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// learnButton listens across every /dev/input/event* device and returns the
// first button/key that is pressed down, so the user can bind a wheel button
// without knowing its evdev code. It returns when a button is pressed or ctx is
// cancelled (the caller sets a timeout).
func learnButton(ctx context.Context) (Button, error) {
	paths, _ := filepath.Glob("/dev/input/event*")
	if len(paths) == 0 {
		return Button{}, fmt.Errorf("no input devices found under /dev/input")
	}
	res := make(chan Button, 1)
	var files []*os.File
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue // no permission / busy — skip
		}
		files = append(files, f)
		go func(path string, f *os.File) {
			buf := make([]byte, inputEventSize)
			for {
				if _, err := io.ReadFull(f, buf); err != nil {
					return
				}
				typ, code, val := decodeEvent(buf)
				if typ == evKey && val == keyValueDown {
					select {
					case res <- Button{Device: path, Code: int(code), Name: deviceName(f)}:
					default:
					}
					return
				}
			}
		}(p, f)
	}
	// Closing the files unblocks all reader goroutines on return.
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if len(files) == 0 {
		return Button{}, fmt.Errorf("no readable input devices (need input-group / udev access)")
	}
	select {
	case b := <-res:
		return b, nil
	case <-ctx.Done():
		return Button{}, fmt.Errorf("no button pressed: %w", ctx.Err())
	}
}

// deviceName reads the evdev device name via the EVIOCGNAME ioctl, best-effort.
func deviceName(f *os.File) string {
	buf := make([]byte, 256)
	// EVIOCGNAME(len) = _IOC(_IOC_READ, 'E', 0x06, len)
	const iocRead = 2
	req := uintptr(iocRead<<30 | len(buf)<<16 | 'E'<<8 | 0x06)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return ""
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

// ensureFIFO creates the FIFO at path if it does not already exist.
func ensureFIFO(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil && !os.IsExist(err) {
		return fmt.Errorf("voice: create fifo %s: %w", path, err)
	}
	return nil
}
