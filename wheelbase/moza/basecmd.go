package moza

import (
	"fmt"
	"math"
)

// readOnlyGroup marks a command that must never be written.
const readOnlyGroup = 0xff

// gainScale converts the game-effect gains, which the main device stores as a
// single 0..255 byte, to the percentage Boxflat and Pit House both display:
// 128 reads as 50%.
const gainScale = 100.0 / 255.0

// The wheelbase base device and its command groups.
//
// These are settings that PERSIST on the hardware, unlike the rim LED path which
// only streams live output. Reading and writing them is a request/response
// transaction; see BaseClient.
const (
	baseDevice = 0x13 // "base" in Boxflat's command database (19 decimal)
	// mainDevice carries the game-effect gains and FFB interpolation. Boxflat
	// shows them on the same pages as the base settings, but they are a different
	// device on the wire, and — unlike the base — they use ONE group for both
	// directions with a different command id for read and write.
	mainDevice = 0x12 // "main" (18 decimal)
	mainGroup  = 0x1f // 31 decimal, both directions

	baseReadGroup  = 0x28 // 40 decimal
	baseWriteGroup = 0x29 // 41 decimal
	// baseStatusGroup is read-only device state: base/error state and the MCU,
	// MOSFET and motor temperatures. Safe to poll, and the cheapest way to prove
	// the transaction layer against real hardware before any setting is written.
	baseStatusGroup = 0x2b // 43 decimal
	// musicGroup carries the startup chime commands. It also carries calibration,
	// so it is read freely but written only with deliberate care.
	musicGroup = 0x2a // 42 decimal
)

// ValueKind is how a command's payload is interpreted.
type ValueKind int

const (
	// KindInt is a big-endian unsigned integer of Bytes width.
	KindInt ValueKind = iota
	// KindBool is an integer constrained to 0 or 1.
	KindBool
	// KindEnum is an integer whose meaning is a labelled set; Labels holds them.
	KindEnum
)

// String names a kind for the frontend.
func (k ValueKind) String() string {
	switch k {
	case KindBool:
		return "bool"
	case KindEnum:
		return "enum"
	default:
		return "int"
	}
}

// BaseCommand describes one wheelbase setting.
//
// It carries the wire details *and* the presentation metadata deliberately.
// Range, unit and label belong next to the command id rather than duplicated
// into Go validation and React form definitions, because that duplication is
// exactly how a UI ends up offering a value the hardware rejects.
type BaseCommand struct {
	// Key is the stable identifier used by config, storage and the frontend.
	Key string
	// Name is the human label.
	Name string
	// Device is the device the command addresses. Zero means the wheelbase.
	Device uint8
	// ReadGroup/WriteGroup are the request groups. Zero means the base groups.
	ReadGroup, WriteGroup uint8
	// ID is the command id for both directions. The main device uses different
	// ids per direction; ReadID/WriteID override it there.
	ID              []uint8
	ReadID, WriteID []uint8
	// Bytes is the payload width in bytes.
	Bytes int
	Kind  ValueKind

	// Scale and Offset convert the raw wire value to the displayed one:
	//
	//	display = raw*Scale + Offset
	//
	// Zero Scale means 1, so a command with neither is passed through untouched.
	//
	// These are not cosmetic. The wheelbase stores steering angle per side and
	// most percentage-like settings in tenths, so a UI showing the raw number is
	// wrong by a factor of two or ten — which is how this page first reported
	// 450% FFB strength and a 450 degree rotation limit. Offset exists because at
	// least one setting is affine rather than scaled: soft limit strength runs
	// 56/78/100 for its three positions. Min and Max below are therefore in
	// DISPLAY units, and encode/decode convert.
	Scale  float64
	Offset float64

	// Min and Max bound the value. These are the app's safety envelope, not
	// necessarily the firmware's: they start conservative and widen only after
	// readback confirms a wider range on real hardware.
	Min, Max int
	// Unit is a display suffix ("%", "deg", "km/h"); empty for unitless.
	Unit string
	// Labels names the values of a KindEnum command, indexed from Min.
	Labels []string
	// Verified records whether this command's range and scaling have been
	// confirmed against hardware. Unverified commands are readable but should
	// not be offered as writable in the UI without a warning — the ranges below
	// come from Boxflat's database and cross-checks, not from an R12 V2.
	Verified bool
	// Affects names other settings this command rewrites as a side effect.
	//
	// Road sensitivity is a macro on this firmware: changing it also moves the
	// equalizer bands. A preset that sets both must therefore write this one
	// first, or the macro overwrites the explicit band values that were just
	// applied — and the driver ends up with an equalizer they did not ask for
	// and cannot see they did not get.
	Affects []string
	// Note carries anything a reader needs before trusting the row.
	Note string
}

// device returns the addressed device, defaulting to the wheelbase.
func (c BaseCommand) device() uint8 {
	if c.Device == 0 {
		return baseDevice
	}
	return c.Device
}

// readGroup and writeGroup default to the base settings groups.
func (c BaseCommand) readGroup() uint8 {
	if c.ReadGroup == 0 {
		return baseReadGroup
	}
	return c.ReadGroup
}

func (c BaseCommand) writeGroup() uint8 {
	if c.WriteGroup == 0 {
		return baseWriteGroup
	}
	return c.WriteGroup
}

// readID and writeID default to ID, which is the same in both directions for
// every base command.
func (c BaseCommand) readID() []uint8 {
	if len(c.ReadID) > 0 {
		return c.ReadID
	}
	return c.ID
}

func (c BaseCommand) writeID() []uint8 {
	if len(c.WriteID) > 0 {
		return c.WriteID
	}
	return c.ID
}

// scale returns the raw-to-display factor, defaulting to 1.
func (c BaseCommand) scale() float64 {
	if c.Scale == 0 {
		return 1
	}
	return c.Scale
}

// Writable reports whether the command can be written. A few are read-only.
func (c BaseCommand) Writable() bool { return len(c.writeID()) > 0 && c.WriteGroup != readOnlyGroup }

// Validate checks a value against the command's envelope.
func (c BaseCommand) Validate(value int) error {
	switch c.Kind {
	case KindBool:
		if value != 0 && value != 1 {
			return fmt.Errorf("%s: %d is not 0 or 1", c.Key, value)
		}
		return nil
	default:
		if value < c.Min || value > c.Max {
			return fmt.Errorf("%s: %d out of range %d..%d", c.Key, value, c.Min, c.Max)
		}
		return nil
	}
}

// encode renders a DISPLAY value as the command's big-endian wire payload.
func (c BaseCommand) encode(value int) ([]byte, error) {
	if err := c.Validate(value); err != nil {
		return nil, err
	}
	raw := int(math.Round((float64(value) - c.Offset) / c.scale()))
	if raw < 0 || raw > 1<<(8*c.Bytes)-1 {
		return nil, fmt.Errorf("%s: %d scales to %d, which does not fit %d byte(s)", c.Key, value, raw, c.Bytes)
	}
	switch c.Bytes {
	case 1:
		return []byte{uint8(raw)}, nil
	case 2:
		return []byte{uint8(raw >> 8), uint8(raw)}, nil
	default:
		return nil, fmt.Errorf("%s: unsupported width %d", c.Key, c.Bytes)
	}
}

// decode reads a value from a payload. It tolerates a payload longer than the
// declared width, since some replies carry trailing bytes.
func (c BaseCommand) decode(payload []byte) (int, error) {
	if len(payload) < c.Bytes {
		return 0, fmt.Errorf("%s: reply carried %d bytes, want %d", c.Key, len(payload), c.Bytes)
	}
	var raw int
	switch c.Bytes {
	case 1:
		raw = int(payload[0])
	case 2:
		raw = int(payload[0])<<8 | int(payload[1])
	default:
		return 0, fmt.Errorf("%s: unsupported width %d", c.Key, c.Bytes)
	}
	return int(math.Round(float64(raw)*c.scale() + c.Offset)), nil
}

// baseCommands is the registry.
//
// Every range here is PROVISIONAL unless Verified is true.
//
// Two sources, and they answer different questions. Boxflat's serial.yml gives
// the wire format only — every command is "type: int" there, with no ranges and
// no indication that a value is a switch or a three-way choice. The presentation
// comes from Boxflat's own UI code (boxflat/panels/base.py), which is where the
// switches, the toggle groups and the slider bounds actually live. Reading only
// the YAML is how "hands-off protection" first appeared here as a 0..100 slider
// when it is a switch.
//
// Verified means the conversion is known, from one of two sources, and each
// entry says which where it matters:
//
//   - OBSERVED on a real R12 V2 against Boxflat's display (the M2 side-by-side).
//   - READ FROM BOXFLAT'S UI SOURCE (boxflat/panels/base.py), which is where the
//     slider bounds and the set_expression/set_reverse_expression transforms
//     actually live. In that file `expression` is display -> raw (the write) and
//     `reverse_expression` is raw -> display (the read).
//
// A single observed point is NOT a confirmed conversion. Soft limit stiffness
// and road sensitivity were both "confirmed" at one reading where the wrong
// formula happened to agree with the right one, and both were writing wrong
// values everywhere else until the source was read. A test pins each at a
// second point for that reason.
//
// The one command still unverified (natural_inertia_enabled) is in serial.yml
// but bound to no control in Boxflat's UI, so nothing says what it does.
var baseCommands = []BaseCommand{
	// --- Safety envelope. Written first by a grouped apply, so that an apply
	// interrupted part-way leaves the base more restricted rather than less.
	{
		Key: "limit_angle", Name: "Wheel rotation angle", ID: []uint8{0x01}, Bytes: 2,
		Min: 90, Max: 2700, Unit: "deg", Scale: 2, Verified: true,
		Note: "the base stores the angle per side; the displayed figure is total lock to lock",
	},
	{
		Key: "torque", Name: "Base torque output", ID: []uint8{0x12}, Bytes: 2,
		Min: 50, Max: 100, Unit: "%", Verified: true,
		Note: "unscaled; floor of 50 matches Boxflat's own slider",
	},
	{
		Key: "speed", Name: "Maximum wheel speed", ID: []uint8{0x0a}, Bytes: 2,
		Min: 0, Max: 200, Scale: 0.1, Verified: true,
	},

	// --- Force feedback.
	{Key: "ffb_strength", Name: "Game FFB strength", ID: []uint8{0x02}, Bytes: 2, Min: 0, Max: 100, Unit: "%", Scale: 0.1, Verified: true},
	{
		Key: "ffb_reverse", Name: "FFB reverse", ID: []uint8{0x18}, Bytes: 2, Kind: KindBool, Verified: true,
		Note: "flips the direction of every force — only for a game that sends it inverted",
	},

	// --- Mechanical feel.
	{Key: "damper", Name: "Wheel damper", ID: []uint8{0x07}, Bytes: 2, Min: 0, Max: 100, Unit: "%", Scale: 0.1, Verified: true},
	{Key: "friction", Name: "Wheel friction", ID: []uint8{0x08}, Bytes: 2, Min: 0, Max: 100, Unit: "%", Scale: 0.1, Verified: true},
	{Key: "spring", Name: "Wheel spring", ID: []uint8{0x09}, Bytes: 2, Min: 0, Max: 100, Unit: "%", Scale: 0.1, Verified: true},
	{
		// Range from Boxflat's own slider (100..500, step 50), which a base
		// reporting 325 sits comfortably inside — the earlier 0..100 was wrong and
		// made the value unrestorable. Deliberately no unit: Boxflat's source
		// carries a "%" suffix but the running app does not show one, and a wrong
		// unit is worse than none.
		Key: "inertia", Name: "Natural inertia", ID: []uint8{0x04}, Bytes: 2,
		Min: 100, Max: 500, Scale: 0.1, Verified: true,
		Note: "Boxflat calls id 0x04 natural inertia and id 0x13 steering wheel inertia; the names are the other way round to what the protocol notes suggest",
	},
	{
		Key: "natural_inertia", Name: "Steering wheel inertia", ID: []uint8{0x13}, Bytes: 2,
		Min: 100, Max: 4000, Verified: true,
		Note: "unscaled; takes effect with hands-off protection on (Boxflat greys it out otherwise)",
	},
	{
		// Deliberately NOT verified. serial.yml lists it (under a "hands-off
		// protection" comment) but no control in Boxflat's UI is bound to it, so
		// there is no evidence of what it does or how it is encoded. Writing an
		// orphan command on a guess is the one thing this registry exists to stop.
		Key: "natural_inertia_enabled", Name: "Natural inertia enable", ID: []uint8{0x16}, Bytes: 2, Kind: KindBool,
		Note: "in Boxflat's command list but used by none of its controls, so its meaning is unknown",
	},

	// --- Speed-dependent damping.
	{Key: "speed_damping", Name: "Damping level", ID: []uint8{0x19}, Bytes: 2, Min: 0, Max: 100, Unit: "%", Verified: true},
	{
		Key: "speed_damping_point", Name: "Trigger speed", ID: []uint8{0x1a}, Bytes: 2,
		Min: 0, Max: 400, Unit: "km/h", Verified: true,
		Note: "unscaled 0..400, per Boxflat's slider",
	},

	// --- Soft limit.
	{
		// AFFINE, from Boxflat's source: raw = d*(400/9) - 400/9 + 100, so the
		// slider's 1..10 lands on 100..500 and Boxflat's reset default of 278 is 5.
		// This was once Scale 0.01, "confirmed" by a base reading 100 as 1 — the
		// one point where both formulas agree. Every other value was written
		// wrong: 10 went out as 1000, twice the top of the real range.
		Key: "soft_limit_stiffness", Name: "Soft limit stiffness", ID: []uint8{0x1f}, Bytes: 2,
		Min: 1, Max: 10, Scale: 9.0 / 400.0, Offset: -1.25, Verified: true,
	},
	{Key: "soft_limit_retain", Name: "Soft limit retains game force", ID: []uint8{0x1c}, Bytes: 2, Kind: KindBool, Verified: true},
	{
		// Affine rather than scaled: the three positions are 56, 78 and 100 on the
		// wire (Boxflat writes index*22+56). Confirmed by a base set to Middle
		// reporting 78.
		Key: "soft_limit_strength", Name: "Soft limit strength", ID: []uint8{0x1b}, Bytes: 2, Kind: KindEnum,
		Min: 0, Max: 2, Labels: []string{"Soft", "Middle", "Hard"},
		Scale: 1.0 / 22.0, Offset: -56.0 / 22.0, Verified: true,
	},

	// --- Road feel / equalizer. Six bands is the Boxflat baseline; four further
	// ids exist on newer 10-band firmware and are gated behind capability
	// detection rather than assumed present.
	{
		// AFFINE, from Boxflat's source: raw = d*4 + 10, so 0..10 is 10..50. Once
		// Scale 0.2, "confirmed" by a base reading 50 as 10 — again the one point
		// where the wrong formula agrees. Road sensitivity 0 was being written as
		// raw 0, which is below anything Boxflat ever sends.
		//
		// Affects is kept for its ORDERING, which is harmless either way, but the
		// macro is unconfirmed: Boxflat writes an EQ preset itself whenever this
		// slider moves (_set_eq_preset), which is what was observed. Whether the
		// firmware ALSO moves the bands on its own is what decides whether this app
		// must write the preset too. Test: write only road sensitivity from this
		// app, re-read, and see whether the bands moved.
		Key: "road_sensitivity", Name: "Road sensitivity", ID: []uint8{0x0c}, Bytes: 2,
		Min: 0, Max: 10, Scale: 0.25, Offset: -2.5, Verified: true,
		Affects: []string{"equalizer1", "equalizer2", "equalizer3", "equalizer4", "equalizer5", "equalizer6"},
		Note:    "Boxflat also rewrites the equalizer bands when this moves; whether the base does so itself is unconfirmed",
	},
	{Key: "equalizer1", Name: "Equalizer 10 Hz", ID: []uint8{0x0e}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},
	{Key: "equalizer2", Name: "Equalizer 15 Hz", ID: []uint8{0x0f}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},
	{Key: "equalizer3", Name: "Equalizer 25 Hz", ID: []uint8{0x10}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},
	{Key: "equalizer4", Name: "Equalizer 40 Hz", ID: []uint8{0x11}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},
	{Key: "equalizer5", Name: "Equalizer 60 Hz", ID: []uint8{0x14}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},
	{Key: "equalizer6", Name: "Equalizer 100 Hz", ID: []uint8{0x2c}, Bytes: 2, Min: 0, Max: 400, Unit: "%", Verified: true},

	// --- Protection.
	{
		Key: "protection", Name: "Hands-off protection", ID: []uint8{0x0d}, Bytes: 2, Kind: KindBool, Verified: true,
		Note: "a switch in Boxflat, not a strength",
	},
	{
		Key: "protection_mode", Name: "Protection mode", ID: []uint8{0x2d}, Bytes: 2, Kind: KindEnum,
		Min: 1, Max: 2, Labels: []string{"Mode 1", "Mode 2"}, Verified: true,
		Note: "1-based (a base on Mode 2 reports 2); takes effect with hands-off protection on",
	},
	// --- Main device. Boxflat shows these on the same pages, but they are a
	// different device and use one group with a different id per direction.
	{
		Key: "interpolation", Name: "FFB interpolation", Bytes: 1,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x4d}, WriteID: []uint8{0x4c},
		// Tenths, from Boxflat's source (expression *10). Previously passed through
		// unscaled, which agrees only at 0 — Boxflat's default, so it looked right.
		Min: 0, Max: 10, Scale: 0.1, Verified: true,
		Note: "Boxflat's own label for it is \"mostly causes issues\"",
	},
	{
		Key: "game_damper", Name: "Game damper", Bytes: 1,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x4f, 0x09}, WriteID: []uint8{0x4e, 0x09},
		Min: 0, Max: 100, Unit: "%", Scale: gainScale, Verified: true,
	},
	{
		Key: "game_friction", Name: "Game friction", Bytes: 1,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x4f, 0x0b}, WriteID: []uint8{0x4e, 0x0b},
		Min: 0, Max: 100, Unit: "%", Scale: gainScale, Verified: true,
	},
	{
		Key: "game_inertia", Name: "Game inertia", Bytes: 1,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x4f, 0x0a}, WriteID: []uint8{0x4e, 0x0a},
		Min: 0, Max: 100, Unit: "%", Scale: gainScale, Verified: true,
	},
	{
		Key: "game_spring", Name: "Game spring", Bytes: 1,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x4f, 0x08}, WriteID: []uint8{0x4e, 0x08},
		Min: 0, Max: 100, Unit: "%", Scale: gainScale, Verified: true,
	},

	// --- FFB curve. Output at five input positions, plus "stronger around
	// center", which slides the first point left. The x positions of points 2..4
	// (ids 0x22 0x02..0x04) are fixed at 40/60/80 by every Boxflat preset and no
	// control moves them, so they stay out.
	{
		// Inverted, from Boxflat's source: raw = 20 - d, so the slider's 0..18
		// moves the first curve point from 20% input down to 2%. A negative scale
		// is the honest encoding — d rises as raw falls.
		Key: "ffb_curve_x1", Name: "Stronger around center", ID: []uint8{0x22, 0x01}, Bytes: 1,
		Min: 0, Max: 18, Scale: -1, Offset: 20, Verified: true,
		Note: "moves the first curve point left of 20% input",
	},
	{Key: "ffb_curve_y1", Name: "Curve output · first point", ID: []uint8{0x22, 0x05}, Bytes: 1, Min: 0, Max: 100, Unit: "%", Verified: true,
		Note: "at 20% input, or further left with stronger around center"},
	{Key: "ffb_curve_y2", Name: "Curve output · 40% input", ID: []uint8{0x22, 0x06}, Bytes: 1, Min: 0, Max: 100, Unit: "%", Verified: true},
	{Key: "ffb_curve_y3", Name: "Curve output · 60% input", ID: []uint8{0x22, 0x07}, Bytes: 1, Min: 0, Max: 100, Unit: "%", Verified: true},
	{Key: "ffb_curve_y4", Name: "Curve output · 80% input", ID: []uint8{0x22, 0x08}, Bytes: 1, Min: 0, Max: 100, Unit: "%", Verified: true},
	{Key: "ffb_curve_y5", Name: "Curve output · 100% input", ID: []uint8{0x22, 0x09}, Bytes: 1, Min: 0, Max: 100, Unit: "%", Verified: true},

	// --- Indicators and startup music. Read here for completeness; the music
	// commands live in group 0x2a, which also carries calibration, so nothing in
	// that group should be written without deliberate care.
	{
		Key: "led_status", Name: "Base status indicator", Bytes: 1, Kind: KindBool, Verified: true,
		Device: mainDevice, ReadGroup: mainGroup, WriteGroup: mainGroup,
		ReadID: []uint8{0x08}, WriteID: []uint8{0x09},
	},
	{
		Key: "music_enabled", Name: "Startup music", Bytes: 1, Kind: KindBool, Verified: true,
		ReadGroup: musicGroup, WriteGroup: musicGroup,
		ReadID: []uint8{0x43, 0x04}, WriteID: []uint8{0x43, 0x03},
	},
	{
		Key: "music_volume", Name: "Startup music volume", Bytes: 1,
		ReadGroup: musicGroup, WriteGroup: musicGroup,
		ReadID: []uint8{0x44, 0x01}, WriteID: []uint8{0x44, 0x00},
		Min: 0, Max: 100, Unit: "%", Scale: gainScale, Verified: true,
	},
	{
		Key: "music_index", Name: "Startup music track", Bytes: 1,
		ReadGroup: musicGroup, WriteGroup: musicGroup,
		ReadID: []uint8{0x43, 0x02}, WriteID: []uint8{0x43, 0x01},
		Min: 1, Max: 10, Verified: true,
		Note: "1-based; Boxflat offers ten tracks",
	},

	{
		Key: "performance_output", Name: "Temperature control strategy", ID: []uint8{0x1e}, Bytes: 2, Kind: KindEnum,
		Min: 0, Max: 1, Labels: []string{"Conservative", "Radical"}, Verified: true,
		Note: "Conservative holds the motor to 50°C, Radical to 60°C",
	},
}

// baseByKey indexes the registry.
var baseByKey = func() map[string]BaseCommand {
	m := make(map[string]BaseCommand, len(baseCommands))
	for _, c := range baseCommands {
		m[c.Key] = c
	}
	return m
}()

// BaseCommands returns the registry in its declared order, which is also the
// order a grouped apply should write: safety envelope first.
func BaseCommands() []BaseCommand {
	out := make([]BaseCommand, len(baseCommands))
	copy(out, baseCommands)
	return out
}

// LookupBaseCommand finds a command by key.
func LookupBaseCommand(key string) (BaseCommand, bool) {
	c, ok := baseByKey[key]
	return c, ok
}

// safetyKeys are written before anything else in a grouped apply. If an apply is
// cut short — a crash, a USB drop, the user quitting — the base is left more
// restricted than either the old or the new preset intended, rather than with a
// previous preset's high torque cap and limits that never arrived.
var safetyKeys = map[string]bool{
	"limit_angle": true,
	"torque":      true,
	"speed":       true,
}

// IsSafetyCommand reports whether a key belongs to the safety envelope.
func IsSafetyCommand(key string) bool { return safetyKeys[key] }
