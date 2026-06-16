package moza

// Profile describes the per-model lighting capabilities of a MOZA base/wheel.
// The one parameter that varies between models and changes how telemetry is
// rendered is the number of RPM rev-light LEDs, so it is captured here rather
// than hard-coded. Add an entry to profilesByProductID to characterise a new
// base; anything unlisted falls back to DefaultProfile (the historical
// behaviour), so detection of an unknown model degrades gracefully.
type Profile struct {
	Model   string
	RPMLEDs int // number of RPM rev-light segments lit by the RPM mask (1..maxRPMLEDs)
}

// maxRPMLEDs bounds the RPM mask width. The telemetry mask is a uint16, so up to
// 16 segments can be addressed.
const maxRPMLEDs = 16

// defaultRPMLEDs is the rev-light count assumed for any base we have not
// explicitly characterised. Ten matches every MOZA base verified so far and the
// original hard-coded behaviour.
const defaultRPMLEDs = 10

// DefaultProfile is used for any MOZA base not present in profilesByProductID.
var DefaultProfile = Profile{Model: "Generic MOZA", RPMLEDs: defaultRPMLEDs}

// profilesByProductID maps a base's USB product ID (under VendorID) to its
// lighting profile. Only models whose LED layout has been verified on real
// hardware belong here; everything else uses DefaultProfile.
var profilesByProductID = map[uint16]Profile{
	0x0006: {Model: "MOZA R12 Base", RPMLEDs: 10},
}

// ProfileFor returns the lighting profile for a base identified by its USB
// product ID. model is the product string from the USB descriptor; it is used
// only to label an otherwise-unknown base so the UI can still name it.
func ProfileFor(productID uint16, model string) Profile {
	if p, ok := profilesByProductID[productID]; ok {
		return p
	}
	p := DefaultProfile
	if model != "" {
		p.Model = model
	}
	return p
}

// clampRPMLEDs normalises a profile's LED count into the addressable range,
// falling back to the default when unset.
func clampRPMLEDs(leds int) int {
	switch {
	case leds <= 0:
		return defaultRPMLEDs
	case leds > maxRPMLEDs:
		return maxRPMLEDs
	default:
		return leds
	}
}
