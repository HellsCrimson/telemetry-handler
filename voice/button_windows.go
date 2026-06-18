//go:build windows

package voice

import (
	"context"
	"fmt"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// On Windows there is no /dev/input; a racing wheel's buttons are read through
// the legacy joystick API in winmm.dll (joyGetPosEx), which returns a button
// bitmask. We poll it to derive press/release edges. The configured
// voice.button_device is the joystick id ("0".."15") and voice.button_code is the
// button bit index — both produced by the LearnVoiceButton flow.
var (
	winmm             = syscall.NewLazyDLL("winmm.dll")
	procJoyGetPosEx   = winmm.NewProc("joyGetPosEx")
	procJoyGetNumDevs = winmm.NewProc("joyGetNumDevs")
)

const (
	joyReturnButtons = 0x00000080 // JOY_RETURNBUTTONS
	joyErrNoError    = 0          // JOYERR_NOERROR
	joyInfoExSize    = 52         // sizeof(JOYINFOEX): 13 DWORDs
	buttonPollEvery  = 16 * time.Millisecond
)

// joyInfoEx mirrors JOYINFOEX (winmm). Only Size/Flags/Buttons are used here.
type joyInfoEx struct {
	Size         uint32
	Flags        uint32
	Xpos         uint32
	Ypos         uint32
	Zpos         uint32
	Rpos         uint32
	Upos         uint32
	Vpos         uint32
	Buttons      uint32
	ButtonNumber uint32
	POV          uint32
	Reserved1    uint32
	Reserved2    uint32
}

func joyGetNumDevs() uint32 {
	r, _, _ := procJoyGetNumDevs.Call()
	return uint32(r)
}

// readJoyButtons returns the button bitmask for joystick id, and false if the
// device is not connected / not readable.
func readJoyButtons(id uint32) (uint32, bool) {
	info := joyInfoEx{Size: joyInfoExSize, Flags: joyReturnButtons}
	r, _, _ := procJoyGetPosEx.Call(uintptr(id), uintptr(unsafe.Pointer(&info)))
	if uint32(r) != joyErrNoError {
		return 0, false
	}
	return info.Buttons, true
}

// ButtonTrigger polls a single joystick button and emits press/release edges.
type ButtonTrigger struct{ events chan Event }

func (t *ButtonTrigger) Events() <-chan Event { return t.events }

func newButtonTrigger(ctx context.Context, device string, code int) (*ButtonTrigger, error) {
	id, err := strconv.Atoi(device)
	if err != nil || id < 0 {
		return nil, fmt.Errorf("voice: button_device must be a joystick id 0-15 (run LearnVoiceButton); got %q", device)
	}
	if _, ok := readJoyButtons(uint32(id)); !ok {
		return nil, fmt.Errorf("voice: joystick %d not connected", id)
	}
	t := &ButtonTrigger{events: make(chan Event, 8)}
	go t.loop(ctx, uint32(id), uint32(code))
	return t, nil
}

func (t *ButtonTrigger) loop(ctx context.Context, id, bit uint32) {
	defer close(t.events)
	mask := uint32(1) << bit
	ticker := time.NewTicker(buttonPollEvery)
	defer ticker.Stop()
	var pressed bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			buttons, ok := readJoyButtons(id)
			if !ok {
				continue
			}
			down := buttons&mask != 0
			switch {
			case down && !pressed:
				pressed = true
				t.emit(ctx, EventPress)
			case !down && pressed:
				pressed = false
				t.emit(ctx, EventRelease)
			}
		}
	}
}

func (t *ButtonTrigger) emit(ctx context.Context, ev Event) {
	select {
	case t.events <- ev:
	case <-ctx.Done():
	}
}

// learnButton polls every joystick and returns the first button found pressed,
// so the user can bind a wheel-rim button without knowing its index.
func learnButton(ctx context.Context) (Button, error) {
	n := joyGetNumDevs()
	if n == 0 {
		return Button{}, fmt.Errorf("no joystick interface available (winmm joyGetNumDevs returned 0)")
	}
	ticker := time.NewTicker(buttonPollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return Button{}, fmt.Errorf("no button pressed: %w", ctx.Err())
		case <-ticker.C:
			for id := uint32(0); id < n; id++ {
				buttons, ok := readJoyButtons(id)
				if !ok || buttons == 0 {
					continue
				}
				bit := lowestSetBit(buttons)
				return Button{
					Device: strconv.Itoa(int(id)),
					Code:   bit,
					Name:   fmt.Sprintf("Joystick %d button %d", id, bit),
				}, nil
			}
		}
	}
}

func lowestSetBit(v uint32) int {
	for i := range 32 {
		if v&(uint32(1)<<i) != 0 {
			return i
		}
	}
	return 0
}

// ensureFIFO is unsupported on Windows; the button trigger is the path there.
func ensureFIFO(string) error {
	return fmt.Errorf("voice: FIFO trigger is not supported on Windows; use the button trigger")
}
