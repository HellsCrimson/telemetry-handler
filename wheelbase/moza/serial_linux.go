//go:build linux

package moza

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type serialConn struct {
	file *os.File
}

type Options struct {
	Port          string
	UpdateHz      float64
	RPMBrightness uint8
	RPMColors     [10]RGB
	ButtonColors  [10]RGB
	ButtonMask    uint16
	// RPMLEDs is the wheel's rev-light segment count (Profile.RPMLEDs). Zero
	// means "use the default", so an Options built without a profile keeps the
	// historical behaviour.
	RPMLEDs int
	// Protocol selects the rim LED protocol (ProtocolOld by default). Newer rims
	// such as the ESX need ProtocolNew; see protocol_new.go.
	Protocol Protocol
	// RPMCurve reshapes the RPM→LED mapping (linear by default; see curve.go).
	RPMCurve RPMCurve
}

type Driver struct {
	mu         sync.Mutex
	conn       *serialConn
	updateMin  time.Duration
	lastUpdate time.Time
	lastMask   uint16
	buttonMask uint16
	rpmLEDs    int
	curve      RPMCurve
	// protocol/lastMaskNew drive the new-protocol path (see update_new.go).
	// lastMaskNew starts at an impossible value so the first update always writes.
	// The colour palette is applied from setupOpts at setup, so it is not stored
	// separately here.
	protocol    Protocol
	lastMaskNew uint32
	// port and setupOpts let the driver transparently reopen the serial
	// device after a transient USB failure (see reconnect.go).
	port       string
	setupOpts  Options
	lastReopen time.Time
}

func openSerial(path string) (*serialConn, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), path)
	conn := &serialConn{file: file}
	if err := configureSerial(fd); err != nil {
		file.Close()
		return nil, err
	}
	return conn, nil
}

func (c *serialConn) Close() error {
	return c.file.Close()
}

func (c *serialConn) WriteFrame(frame []byte) error {
	n, err := c.file.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return fmt.Errorf("short serial write: wrote %d of %d bytes", n, len(frame))
	}
	return nil
}

// read reads available bytes, blocking up to the termios VTIME (0.5s) before
// returning. Used by DetectWheel to read query responses.
func (c *serialConn) read(p []byte) (int, error) {
	return c.file.Read(p)
}

func configureSerial(fd int) error {
	var termios syscall.Termios
	if err := ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios))); err != nil {
		return err
	}

	termios.Iflag = syscall.IGNPAR
	termios.Oflag = 0
	termios.Lflag = 0
	termios.Cflag = syscall.B115200 | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	termios.Cc[syscall.VMIN] = 0
	termios.Cc[syscall.VTIME] = 5
	termios.Ispeed = syscall.B115200
	termios.Ospeed = syscall.B115200

	return ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&termios)))
}

func ioctl(fd int, request uint, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(request), arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func RunLightTest(path string, duration time.Duration, protocol Protocol) error {
	if duration <= 0 {
		return fmt.Errorf("duration must be greater than zero")
	}

	conn, err := openSerial(path)
	if err != nil {
		return err
	}
	defer conn.Close()

	if protocol == ProtocolNew {
		// Sweep the bar with the default green→red gradient so the rev lights are
		// obviously responding.
		return runLightTestNew(conn, defaultRPMGradient(), duration)
	}

	colors := [10]RGB{}
	for i := range colors {
		if i%2 == 0 {
			colors[i] = RGB{R: 255, G: 0, B: 0}
		} else {
			colors[i] = RGB{R: 0, G: 0, B: 255}
		}
	}

	frames, err := setupLightTestFrames(colors)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := conn.WriteFrame(frame); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(duration)

	index := 0
	for {
		select {
		case <-deadline:
			if err := writeMask(conn, 0); err != nil {
				return err
			}
			mode, err := setTelemetryMode(false)
			if err != nil {
				return err
			}
			return conn.WriteFrame(mode)
		case <-ticker.C:
			mask := uint16(1 << (index % 10))
			if err := writeMask(conn, mask); err != nil {
				return err
			}
			index++
		}
	}
}

func NewDriver(options Options) (*Driver, error) {
	if options.Port == "" {
		return nil, fmt.Errorf("port must not be empty")
	}
	if options.UpdateHz <= 0 {
		return nil, fmt.Errorf("update hz must be greater than zero")
	}
	if options.RPMBrightness > 15 {
		options.RPMBrightness = 15
	}
	options.ButtonMask &= 0x03ff

	conn, err := openSerial(options.Port)
	if err != nil {
		return nil, err
	}

	driver := &Driver{
		conn:        conn,
		updateMin:   time.Duration(float64(time.Second) / options.UpdateHz),
		lastMask:    ^uint16(0),
		buttonMask:  options.ButtonMask,
		rpmLEDs:     options.RPMLEDs,
		curve:       options.RPMCurve,
		protocol:    options.Protocol,
		lastMaskNew: ^uint32(0),
		port:        options.Port,
		setupOpts:   options,
	}
	if err := driver.setup(options); err != nil {
		conn.Close()
		return nil, err
	}
	return driver, nil
}

func (d *Driver) Close() error {
	if d == nil || d.conn == nil {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	err := d.blankLEDs()
	closeErr := d.conn.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (d *Driver) UpdateRPM(currentRPM, maxRPM float32) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.protocol == ProtocolNew {
		return d.updateRPMNew(currentRPM, maxRPM)
	}

	mask := rpmMask(currentRPM, maxRPM, d.rpmLEDs, d.curve)
	now := time.Now()
	if mask == d.lastMask && now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}
	if now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}

	if err := d.writeMasksWithRetry(mask, d.buttonMask); err != nil {
		return err
	}
	d.lastMask = mask
	d.lastUpdate = now
	return nil
}

func (d *Driver) Apply(options Options) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if options.UpdateHz <= 0 {
		return fmt.Errorf("update hz must be greater than zero")
	}
	if options.RPMBrightness > 15 {
		options.RPMBrightness = 15
	}

	options.ButtonMask &= 0x03ff
	d.updateMin = time.Duration(float64(time.Second) / options.UpdateHz)
	d.buttonMask = options.ButtonMask
	d.rpmLEDs = options.RPMLEDs
	d.curve = options.RPMCurve
	d.protocol = options.Protocol
	d.setupOpts = options
	d.lastMask = ^uint16(0)
	d.lastMaskNew = ^uint32(0)
	d.lastUpdate = time.Time{}
	return d.setup(options)
}

func (d *Driver) setup(options Options) error {
	frames, err := setupFramesFor(options)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := d.conn.WriteFrame(frame); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}
	return nil
}

func (d *Driver) writeMasks(rpmMask, buttonMask uint16) error {
	rpm, err := setRPMTelemetryMask(rpmMask)
	if err != nil {
		return err
	}
	if err := d.conn.WriteFrame(rpm); err != nil {
		return err
	}

	buttons, err := setButtonTelemetryMask(buttonMask)
	if err != nil {
		return err
	}
	return d.conn.WriteFrame(buttons)
}

func writeMask(conn *serialConn, mask uint16) error {
	rpm, err := setRPMTelemetryMask(mask)
	if err != nil {
		return err
	}
	if err := conn.WriteFrame(rpm); err != nil {
		return err
	}

	buttons, err := setButtonTelemetryMask(mask)
	if err != nil {
		return err
	}
	return conn.WriteFrame(buttons)
}
