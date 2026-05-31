//go:build linux

package moza

import (
	"fmt"
	"os"
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
}

type Driver struct {
	conn       *serialConn
	updateMin  time.Duration
	lastUpdate time.Time
	lastMask   uint16
	buttonMask uint16
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

func RunLightTest(path string, duration time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("duration must be greater than zero")
	}

	conn, err := openSerial(path)
	if err != nil {
		return err
	}
	defer conn.Close()

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
		conn:       conn,
		updateMin:  time.Duration(float64(time.Second) / options.UpdateHz),
		lastMask:   ^uint16(0),
		buttonMask: options.ButtonMask,
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

	err := d.writeMasks(0, 0)
	mode, modeErr := setTelemetryMode(false)
	if modeErr == nil {
		modeErr = d.conn.WriteFrame(mode)
	}
	closeErr := d.conn.Close()
	if err != nil {
		return err
	}
	if modeErr != nil {
		return modeErr
	}
	return closeErr
}

func (d *Driver) UpdateRPM(currentRPM, maxRPM float32) error {
	mask := rpmMask(currentRPM, maxRPM)
	now := time.Now()
	if mask == d.lastMask && now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}
	if now.Sub(d.lastUpdate) < d.updateMin {
		return nil
	}

	if err := d.writeMasks(mask, d.buttonMask); err != nil {
		return err
	}
	d.lastMask = mask
	d.lastUpdate = now
	return nil
}

func (d *Driver) setup(options Options) error {
	frames, err := setupTelemetryFrames(options.RPMBrightness, options.RPMColors, options.ButtonColors, options.ButtonMask)
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

func setupLightTestFrames(colors [10]RGB) ([][]byte, error) {
	return setupTelemetryFrames(15, colors, colors, 0x03ff)
}

func setupTelemetryFrames(brightness uint8, rpmColors [10]RGB, buttonColors [10]RGB, buttonMask uint16) ([][]byte, error) {
	frames := make([][]byte, 0, 5)

	mode, err := setTelemetryMode(true)
	if err != nil {
		return nil, err
	}
	frames = append(frames, mode)

	brightnessFrame, err := setRPMBrightness(brightness)
	if err != nil {
		return nil, err
	}
	frames = append(frames, brightnessFrame)

	colorFrames, err := setRPMTelemetryColors(rpmColors)
	if err != nil {
		return nil, err
	}
	frames = append(frames, colorFrames...)

	buttonColorFrames, err := setButtonTelemetryColors(buttonColors)
	if err != nil {
		return nil, err
	}
	frames = append(frames, buttonColorFrames...)

	off, err := setRPMTelemetryMask(0)
	if err != nil {
		return nil, err
	}
	frames = append(frames, off)

	buttons, err := setButtonTelemetryMask(buttonMask)
	if err != nil {
		return nil, err
	}
	frames = append(frames, buttons)

	return frames, nil
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

func rpmMask(currentRPM, maxRPM float32) uint16 {
	if currentRPM <= 0 || maxRPM <= 0 {
		return 0
	}

	ratio := currentRPM / maxRPM
	if ratio <= 0 {
		return 0
	}
	if ratio > 1 {
		ratio = 1
	}

	lit := int(ratio * 10)
	if lit < 1 {
		lit = 1
	}
	if lit > 10 {
		lit = 10
	}

	return uint16((1 << lit) - 1)
}
