package moza

// VendorID is the USB vendor ID Gudsen uses for all MOZA Racing hardware. The
// connected base/wheel is identified by reading the USB device descriptor (the
// serial command protocol in protocol.go is write-only and cannot report
// identity), so detection keys off this vendor and the per-device product ID.
const VendorID = 0x346e

// Device describes a connected MOZA USB device discovered by Detect. Port is the
// serial device path the driver opens (e.g. /dev/ttyACM1 on Linux); Model is the
// USB product string the device reports ("MOZA R12 Base"); Serial is the unique
// per-unit serial number. ProductID is the USB product ID, used to look up a
// lighting Profile (see profile.go).
type Device struct {
	Port      string `json:"port"`
	Model     string `json:"model"`
	Serial    string `json:"serial"`
	ProductID uint16 `json:"product_id"`
}

// Detect enumerates connected MOZA devices. Implemented per platform (sysfs on
// Linux; a stub elsewhere). It never returns hardware-level errors as fatal: an
// empty slice simply means no MOZA wheel is currently attached.
func Detect() ([]Device, error) {
	return detectDevices()
}
