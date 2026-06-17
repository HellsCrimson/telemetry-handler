//go:build windows

package moza

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type serialConn struct {
	handle syscall.Handle
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

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateFileW     = kernel32.NewProc("CreateFileW")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
	procWriteFile       = kernel32.NewProc("WriteFile")
	procReadFile        = kernel32.NewProc("ReadFile")
	procGetCommState    = kernel32.NewProc("GetCommState")
	procSetCommState    = kernel32.NewProc("SetCommState")
	procSetCommTimeouts = kernel32.NewProc("SetCommTimeouts")
	procPurgeComm       = kernel32.NewProc("PurgeComm")
	procGetLastError    = kernel32.NewProc("GetLastError")
)

const (
	invalidHandle = ^syscall.Handle(0)

	genericRead         = 0x80000000
	genericWrite        = 0x40000000
	fileShareRead       = 0x00000001
	fileShareWrite      = 0x00000002
	openExisting        = 3
	fileAttributeNormal = 0x80

	purgeRxAbort = 0x0002
	purgeTxAbort = 0x0001
	purgeRxClear = 0x0008
	purgeTxClear = 0x0004

	cbr115200 = 115200
)

type dcb struct {
	DCBlength  uint32
	BaudRate   uint32
	Flags      uint32
	wReserved  uint16
	XonLim     uint16
	XoffLim    uint16
	ByteSize   byte
	Parity     byte
	StopBits   byte
	XonChar    byte
	XoffChar   byte
	ErrorChar  byte
	EofChar    byte
	EvtChar    byte
	wReserved1 uint16
}

type commTimeouts struct {
	ReadIntervalTimeout         uint32
	ReadTotalTimeoutMultiplier  uint32
	ReadTotalTimeoutConstant    uint32
	WriteTotalTimeoutMultiplier uint32
	WriteTotalTimeoutConstant   uint32
}

func openSerial(path string) (*serialConn, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		genericRead|genericWrite,
		0,
		0,
		openExisting,
		fileAttributeNormal,
		0,
	)

	if handle == uintptr(invalidHandle) {
		return nil, fmt.Errorf("CreateFileW failed: %w", err)
	}

	conn := &serialConn{handle: syscall.Handle(handle)}

	if err := configureSerial(conn); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

func configureSerial(conn *serialConn) error {
	// Get current DCB settings
	dcb := dcb{DCBlength: uint32(unsafe.Sizeof(dcb{}))}
	ok, _, err := procGetCommState.Call(uintptr(conn.handle), uintptr(unsafe.Pointer(&dcb)))
	if ok == 0 {
		return fmt.Errorf("GetCommState failed: %w", err)
	}

	// Configure for 115200 baud, 8 data bits, no parity, 1 stop bit
	dcb.BaudRate = cbr115200
	dcb.ByteSize = 8
	dcb.Parity = 0     // NOPARITY
	dcb.StopBits = 0   // ONESTOPBIT
	dcb.Flags = 0x0001 // fBinary

	ok, _, err = procSetCommState.Call(uintptr(conn.handle), uintptr(unsafe.Pointer(&dcb)))
	if ok == 0 {
		return fmt.Errorf("SetCommState failed: %w", err)
	}

	// Set timeouts
	timeouts := commTimeouts{
		ReadIntervalTimeout:         50,
		ReadTotalTimeoutMultiplier:  0,
		ReadTotalTimeoutConstant:    500,
		WriteTotalTimeoutMultiplier: 0,
		WriteTotalTimeoutConstant:   500,
	}

	ok, _, err = procSetCommTimeouts.Call(uintptr(conn.handle), uintptr(unsafe.Pointer(&timeouts)))
	if ok == 0 {
		return fmt.Errorf("SetCommTimeouts failed: %w", err)
	}

	// Purge any pending data
	ok, _, err = procPurgeComm.Call(uintptr(conn.handle), purgeRxAbort|purgeTxAbort|purgeRxClear|purgeTxClear)
	if ok == 0 {
		return fmt.Errorf("PurgeComm failed: %w", err)
	}

	return nil
}

func (c *serialConn) Close() error {
	if c.handle != invalidHandle {
		ok, _, err := procCloseHandle.Call(uintptr(c.handle))
		if ok == 0 {
			return fmt.Errorf("CloseHandle failed: %w", err)
		}
		c.handle = invalidHandle
	}
	return nil
}

func (c *serialConn) WriteFrame(frame []byte) error {
	if c.handle == invalidHandle {
		return fmt.Errorf("serial port is closed")
	}

	var bytesWritten uint32
	ok, _, err := procWriteFile.Call(
		uintptr(c.handle),
		uintptr(unsafe.Pointer(&frame[0])),
		uintptr(len(frame)),
		uintptr(unsafe.Pointer(&bytesWritten)),
		0,
	)

	if ok == 0 {
		return fmt.Errorf("WriteFile failed: %w", err)
	}

	if bytesWritten != uint32(len(frame)) {
		return fmt.Errorf("short serial write: wrote %d of %d bytes", bytesWritten, len(frame))
	}

	return nil
}

// read reads available bytes, returning after the configured comm read timeout
// (500ms) when none arrive. Used by DetectWheel to read query responses.
func (c *serialConn) read(p []byte) (int, error) {
	if c.handle == invalidHandle {
		return 0, fmt.Errorf("serial port is closed")
	}
	var bytesRead uint32
	ok, _, err := procReadFile.Call(
		uintptr(c.handle),
		uintptr(unsafe.Pointer(&p[0])),
		uintptr(len(p)),
		uintptr(unsafe.Pointer(&bytesRead)),
		0,
	)
	if ok == 0 {
		return 0, fmt.Errorf("ReadFile failed: %w", err)
	}
	return int(bytesRead), nil
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
