//go:build linux

package moza

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// usbDevicesDir is the sysfs directory listing every USB device and interface.
// Device entries (e.g. "5-2") carry the descriptor attributes; the CDC-ACM tty
// lives under the device's interface (e.g. "5-2/5-2:1.0/tty/ttyACM1").
const usbDevicesDir = "/sys/bus/usb/devices"

// detectDevices scans sysfs for USB devices with the MOZA vendor ID and resolves
// each to its serial port. It reads the descriptor attributes directly, so it
// needs no udev, no root, and no open handle on the device.
func detectDevices() ([]Device, error) {
	entries, err := os.ReadDir(usbDevicesDir)
	if err != nil {
		// No USB sysfs (unusual) just means nothing to report, not a hard error.
		return nil, nil
	}

	var devices []Device
	for _, entry := range entries {
		dir := filepath.Join(usbDevicesDir, entry.Name())
		vendor := readSysAttr(dir, "idVendor")
		if vendor == "" {
			continue // an interface dir or a non-device node
		}
		vid, err := strconv.ParseUint(vendor, 16, 16)
		if err != nil || uint16(vid) != VendorID {
			continue
		}

		port := resolveTTY(dir)
		if port == "" {
			continue // a MOZA device that exposes no serial tty (e.g. a hub)
		}

		pid, _ := strconv.ParseUint(readSysAttr(dir, "idProduct"), 16, 16)
		model := readSysAttr(dir, "product")
		if model == "" {
			model = "MOZA device"
		}
		devices = append(devices, Device{
			Port:      port,
			Model:     model,
			Serial:    readSysAttr(dir, "serial"),
			ProductID: uint16(pid),
		})
	}
	return devices, nil
}

// resolveTTY finds the /dev/ttyACM* path for a USB device directory by locating
// the tty node any of its interfaces exposes (CDC-ACM: <dir>/<intf>/tty/ttyACMx).
func resolveTTY(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "tty", "*"))
	if len(matches) == 0 {
		return ""
	}
	return filepath.Join("/dev", filepath.Base(matches[0]))
}

// readSysAttr reads a single-line sysfs attribute, returning "" when absent.
func readSysAttr(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
