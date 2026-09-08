package moza

import (
	"errors"
	"testing"
)

// frameOf builds a well-formed frame the way the base would.
func frameOf(t *testing.T, group, device uint8, id, payload []byte) []byte {
	t.Helper()
	f, err := buildFrame(group, device, id, payload)
	if err != nil {
		t.Fatalf("buildFrame: %v", err)
	}
	return f
}

func TestDecodeFrameRoundTrip(t *testing.T) {
	raw := frameOf(t, baseWriteGroup, baseDevice, []byte{0x02}, []byte{0x00, 0x64})
	f, err := decodeFrame(raw)
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	if f.Group != baseWriteGroup || f.Device != baseDevice {
		t.Errorf("group/device = %#02x/%#02x", f.Group, f.Device)
	}
	if got, want := f.Body, []byte{0x02, 0x00, 0x64}; string(got) != string(want) {
		t.Errorf("body = % x, want % x", got, want)
	}
}

// The checksum must cover the leading 0x7e. Excluding it is an equally natural
// reading of "sum with magic value 0x0d" and produces frames the base ignores,
// so the encoder and decoder are pinned to the same interpretation here.
func TestChecksumCoversTheStartByte(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, nil)

	sumWithout := magicValue
	for _, b := range raw[1 : len(raw)-1] { // deliberately skipping 0x7e
		sumWithout += int(b)
	}
	if uint8(sumWithout%256) == raw[len(raw)-1] {
		t.Fatal("test is vacuous: both interpretations agree on this frame")
	}
	if _, err := decodeFrame(raw); err != nil {
		t.Fatalf("the encoder's own frame must decode: %v", err)
	}
}

func TestDecodeFrameRejectsBadChecksum(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, []byte{0x00, 0x01})
	raw[len(raw)-1] ^= 0xff
	if _, err := decodeFrame(raw); !errors.Is(err, ErrBadChecksum) {
		t.Errorf("expected a checksum error, got %v", err)
	}
}

func TestDecodeFrameWaitsForTheWholeFrame(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, []byte{0x00, 0x01})
	for cut := 1; cut < len(raw); cut++ {
		if _, err := decodeFrame(raw[:cut]); !errors.Is(err, errShortFrame) {
			t.Errorf("truncated to %d bytes: expected errShortFrame, got %v", cut, err)
		}
	}
}

// THE framing hazard. A settings value of 126 is 0x7e — an unremarkable FFB
// percentage — so a reader that delimits by scanning for the next start byte
// will resync inside this frame and decode garbage. The length byte has to be
// what delimits.
func TestScanFrameSurvivesA0x7eInThePayload(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x02}, []byte{0x00, 0x7e}) // value 126
	f, consumed, err := scanFrame(raw)
	if err != nil {
		t.Fatalf("scanFrame: %v", err)
	}
	if consumed != len(raw) {
		t.Errorf("consumed %d of %d bytes", consumed, len(raw))
	}
	payload := f.payloadAfter([]uint8{0x02})
	if len(payload) != 2 || payload[0] != 0x00 || payload[1] != 0x7e {
		t.Errorf("payload = % x, want 00 7e — the frame was resynced mid-payload", payload)
	}
}

// Both bytes of a 16-bit value can be 0x7e (32382), which is inside the
// equalizer range.
func TestScanFrameSurvivesTwo0x7eBytes(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x0e}, []byte{0x7e, 0x7e})
	f, _, err := scanFrame(raw)
	if err != nil {
		t.Fatalf("scanFrame: %v", err)
	}
	if got := f.payloadAfter([]uint8{0x0e}); got[0] != 0x7e || got[1] != 0x7e {
		t.Errorf("payload = % x", got)
	}
}

func TestScanFrameSkipsLeadingNoise(t *testing.T) {
	raw := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, []byte{0x03, 0x84})
	noisy := append([]byte{0x00, 0xff, 0x12}, raw...)
	f, consumed, err := scanFrame(noisy)
	if err != nil {
		t.Fatalf("scanFrame: %v", err)
	}
	if consumed != len(noisy) {
		t.Errorf("consumed %d of %d", consumed, len(noisy))
	}
	if f.Body[0] != 0x01 {
		t.Errorf("wrong frame decoded: % x", f.Body)
	}
}

// A corrupt frame must not swallow the good one behind it: resync is recovery.
func TestScanFrameRecoversAfterACorruptFrame(t *testing.T) {
	bad := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, []byte{0x00, 0x01})
	bad[len(bad)-1] ^= 0xff
	good := frameOf(t, baseReadGroup, baseDevice, []byte{0x02}, []byte{0x00, 0x2a})

	f, consumed, err := scanFrame(append(bad, good...))
	if err != nil {
		t.Fatalf("scanFrame: %v", err)
	}
	if f.Body[0] != 0x02 {
		t.Errorf("expected the good frame, got id %#02x", f.Body[0])
	}
	if consumed != len(bad)+len(good) {
		t.Errorf("consumed %d, want %d", consumed, len(bad)+len(good))
	}
}

func TestScanFrameKeepsAPartialTail(t *testing.T) {
	whole := frameOf(t, baseReadGroup, baseDevice, []byte{0x01}, []byte{0x03, 0x84})
	partial := whole[:len(whole)-2]
	_, consumed, err := scanFrame(partial)
	if !errors.Is(err, errShortFrame) {
		t.Fatalf("expected errShortFrame, got %v", err)
	}
	if consumed != 0 {
		t.Errorf("consumed %d bytes of an incomplete frame; the tail must be kept", consumed)
	}
}

// Confirmed against hardware by DetectWheel: group 0x07 is answered on 0x87,
// device 0x18 on 0x81.
func TestResponseAddressing(t *testing.T) {
	if got := responseGroup(0x07); got != 0x87 {
		t.Errorf("responseGroup(0x07) = %#02x", got)
	}
	if got := responseGroup(baseReadGroup); got != 0xa8 {
		t.Errorf("responseGroup(0x28) = %#02x, want 0xa8", got)
	}
	for _, tc := range []struct{ in, want uint8 }{{0x13, 0x31}, {0x18, 0x81}, {0x17, 0x71}} {
		if got := responseDevice(tc.in); got != tc.want {
			t.Errorf("responseDevice(%#02x) = %#02x, want %#02x", tc.in, got, tc.want)
		}
	}
}

func TestFrameMatches(t *testing.T) {
	reply := Frame{Group: 0xa8, Device: 0x31, Body: []byte{0x02, 0x00, 0x64}}
	if !reply.matches(baseReadGroup, baseDevice, []uint8{0x02}) {
		t.Error("a well-formed reply should match its request")
	}
	if reply.matches(baseReadGroup, baseDevice, []uint8{0x03}) {
		t.Error("a reply to a different command must not match")
	}
	if reply.matches(baseWriteGroup, baseDevice, []uint8{0x02}) {
		t.Error("a read reply must not match a write request")
	}
}
