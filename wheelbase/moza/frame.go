package moza

import (
	"errors"
	"fmt"
)

// Frame is one decoded MOZA message.
//
// The wire shape is the same in both directions, and is what buildFrame emits:
//
//	0x7e, payload_len, group, device, id..., payload..., checksum
//
// payload_len counts the id and payload bytes together, so the whole frame is
// payload_len+5 bytes. ID and Payload are only separable with knowledge of the
// command, so Frame keeps them as one Body and lets the caller split it.
type Frame struct {
	Group  uint8
	Device uint8
	Body   []byte // id bytes followed by payload bytes
}

// Size is the encoded length of a frame with a body of n bytes.
func frameSize(bodyLen int) int { return bodyLen + 5 }

var (
	// errShortFrame means the buffer does not yet hold a whole frame. It is not a
	// failure — the caller should read more bytes and try again.
	errShortFrame = errors.New("moza: incomplete frame")
	// errNoStart means no start byte was found at all.
	errNoStart = errors.New("moza: no frame start")
	// ErrBadChecksum marks a frame whose checksum did not verify. It is exported
	// because a caller counting these is diagnosing a cable or a baud problem.
	ErrBadChecksum = errors.New("moza: bad checksum")
)

// decodeFrame decodes the frame beginning at buf[0].
//
// It requires buf to start on the 0x7e; use scanFrame to find one. The checksum
// is verified over exactly the bytes buildFrame summed — start byte included,
// checksum byte excluded — so a frame that decodes here is a frame the base
// would itself have accepted.
func decodeFrame(buf []byte) (Frame, error) {
	if len(buf) < 2 {
		return Frame{}, errShortFrame
	}
	if buf[0] != messageStart {
		return Frame{}, errNoStart
	}
	bodyLen := int(buf[1])
	total := frameSize(bodyLen)
	if len(buf) < total {
		return Frame{}, errShortFrame
	}
	if got, want := buf[total-1], checksum(buf[:total-1]); got != want {
		return Frame{}, fmt.Errorf("%w: got %#02x want %#02x", ErrBadChecksum, got, want)
	}
	// bodyLen counts id+payload, so a well-formed frame always has group and
	// device present; a zero body is legal (an ack with no id).
	body := make([]byte, bodyLen)
	copy(body, buf[4:total-1])
	return Frame{Group: buf[2], Device: buf[3], Body: body}, nil
}

// scanFrame finds and decodes the first valid frame in buf.
//
// It returns the frame and the number of bytes consumed from the front of buf,
// so the caller can advance its accumulator.
//
// The length byte — not the next 0x7e — delimits the frame. This matters more
// than it looks: settings values are arbitrary uint16, so a payload byte of 0x7e
// is simply the value 126, an unremarkable FFB percentage or equalizer level.
// A reader that scans for the next start byte will resync in the middle of such
// a frame and decode garbage, intermittently and only for particular values.
// Scanning here is strictly error recovery: it advances past bytes that cannot
// begin a valid frame, and only ever after a length-and-checksum check has
// failed.
func scanFrame(buf []byte) (Frame, int, error) {
	for i := 0; i < len(buf); i++ {
		if buf[i] != messageStart {
			continue // leading noise, or the tail of a frame we gave up on
		}
		frame, err := decodeFrame(buf[i:])
		switch {
		case err == nil:
			return frame, i + frameSize(len(frame.Body)), nil
		case errors.Is(err, errShortFrame):
			// A truncated frame at the end of the buffer is normal mid-read: keep
			// the partial bytes and wait for more rather than resyncing past them.
			return Frame{}, i, errShortFrame
		default:
			// Bad checksum, or a 0x7e that was really payload. Step over this byte
			// and look for the next candidate start.
			continue
		}
	}
	return Frame{}, len(buf), errNoStart
}

// responseGroup is the group a reply to req carries: the request group with the
// high bit set. Confirmed against hardware by DetectWheel, which sends group
// 0x07 and matches replies on 0x87.
func responseGroup(requestGroup uint8) uint8 { return requestGroup | 0x80 }

// responseDevice is the device byte a reply carries: the request device with its
// nibbles swapped. Device 0x13 answers as 0x31, 0x18 as 0x81.
func responseDevice(requestDevice uint8) uint8 {
	return requestDevice<<4 | requestDevice>>4
}

// matches reports whether f is the reply to a request sent to the given group,
// device and command id.
func (f Frame) matches(requestGroup, requestDevice uint8, id []uint8) bool {
	if f.Group != responseGroup(requestGroup) || f.Device != responseDevice(requestDevice) {
		return false
	}
	if len(f.Body) < len(id) {
		return false
	}
	for i, b := range id {
		if f.Body[i] != b {
			return false
		}
	}
	return true
}

// payloadAfter returns the frame's payload with the leading command id removed.
func (f Frame) payloadAfter(id []uint8) []byte {
	if len(f.Body) < len(id) {
		return nil
	}
	return f.Body[len(id):]
}
