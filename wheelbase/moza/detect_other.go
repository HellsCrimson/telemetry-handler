//go:build !linux

package moza

// detectDevices has no implementation off Linux yet. On Windows the equivalent
// would enumerate the SetupAPI device tree for the MOZA vendor ID and map each
// to its COM port; until that exists, detection returns nothing and the user
// configures the port manually.
func detectDevices() ([]Device, error) {
	return nil, nil
}
