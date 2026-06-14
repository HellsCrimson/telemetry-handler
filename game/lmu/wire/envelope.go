package wire

import (
	"encoding/binary"
	"fmt"
)

// The wire envelope lets one frame (which can be ~100KB for a full grid, past
// the 64KB UDP datagram limit) span several datagrams. Each datagram is
// [EnvelopeHeader][chunk bytes]; the receiver reassembles them by frame seq.

const (
	// Magic prefixes every datagram so the single shared UDP port can tell a
	// wire chunk apart from Forza's fixed 324-byte binary packet and the legacy
	// lmu-bridge JSON (which starts with '{').
	Magic = "LMU2"

	// ProtocolVersion is bumped on incompatible wire changes.
	ProtocolVersion = uint16(1)

	// EnvelopeHeaderSize is the fixed datagram header length.
	EnvelopeHeaderSize = 24

	// DefaultMaxChunkPayload is the per-datagram payload budget (excluding the
	// envelope header). 60000 keeps a datagram comfortably under the 65507-byte
	// UDP cap while needing only ~2 chunks for a full grid; on the 127.0.0.1
	// loopback the bridge uses (Wine→host) the large datagram is not fragmented.
	DefaultMaxChunkPayload = 60000
)

// envelope is the per-datagram header (24 bytes, little-endian):
//
//	[0:4]   magic "LMU2"
//	[4:6]   version
//	[6:8]   flags (reserved)
//	[8:12]  frameSeq
//	[12:14] chunkIndex
//	[14:16] chunkCount
//	[16:20] totalLen   (reassembled payload length)
//	[20:24] chunkOffset (this chunk's byte offset into the payload)
//
// The chunk length is implicit: len(datagram) - EnvelopeHeaderSize.
type envelope struct {
	version     uint16
	flags       uint16
	frameSeq    uint32
	chunkIndex  uint16
	chunkCount  uint16
	totalLen    uint32
	chunkOffset uint32
}

func (e envelope) encode(chunk []byte) []byte {
	out := make([]byte, EnvelopeHeaderSize+len(chunk))
	copy(out[0:4], Magic)
	binary.LittleEndian.PutUint16(out[4:6], e.version)
	binary.LittleEndian.PutUint16(out[6:8], e.flags)
	binary.LittleEndian.PutUint32(out[8:12], e.frameSeq)
	binary.LittleEndian.PutUint16(out[12:14], e.chunkIndex)
	binary.LittleEndian.PutUint16(out[14:16], e.chunkCount)
	binary.LittleEndian.PutUint32(out[16:20], e.totalLen)
	binary.LittleEndian.PutUint32(out[20:24], e.chunkOffset)
	copy(out[EnvelopeHeaderSize:], chunk)
	return out
}

// IsEnvelope reports whether a datagram is a wire chunk (starts with Magic).
func IsEnvelope(data []byte) bool {
	return len(data) >= EnvelopeHeaderSize && string(data[0:4]) == Magic
}

func decodeEnvelope(data []byte) (envelope, []byte, error) {
	if !IsEnvelope(data) {
		return envelope{}, nil, fmt.Errorf("not a wire datagram")
	}
	e := envelope{
		version:     binary.LittleEndian.Uint16(data[4:6]),
		flags:       binary.LittleEndian.Uint16(data[6:8]),
		frameSeq:    binary.LittleEndian.Uint32(data[8:12]),
		chunkIndex:  binary.LittleEndian.Uint16(data[12:14]),
		chunkCount:  binary.LittleEndian.Uint16(data[14:16]),
		totalLen:    binary.LittleEndian.Uint32(data[16:20]),
		chunkOffset: binary.LittleEndian.Uint32(data[20:24]),
	}
	return e, data[EnvelopeHeaderSize:], nil
}

// Chunk splits a marshalled payload into one or more datagrams ready to send.
// maxChunkPayload <= 0 selects DefaultMaxChunkPayload. An empty payload still
// yields a single (empty) chunk so the receiver observes the frame.
func Chunk(payload []byte, frameSeq uint32, maxChunkPayload int) [][]byte {
	if maxChunkPayload <= 0 {
		maxChunkPayload = DefaultMaxChunkPayload
	}
	total := len(payload)
	count := (total + maxChunkPayload - 1) / maxChunkPayload
	if count == 0 {
		count = 1
	}
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		start := i * maxChunkPayload
		end := min(start+maxChunkPayload, total)
		e := envelope{
			version:     ProtocolVersion,
			frameSeq:    frameSeq,
			chunkIndex:  uint16(i),
			chunkCount:  uint16(count),
			totalLen:    uint32(total),
			chunkOffset: uint32(start),
		}
		out = append(out, e.encode(payload[start:end]))
	}
	return out
}

// Reassembler collects wire chunks into complete payloads. It tracks a single
// in-flight frame: a chunk for a newer frameSeq discards an incomplete older
// one (UDP gives no delivery guarantee, so a dropped chunk must not wedge it).
// It is not safe for concurrent use.
type Reassembler struct {
	have     bool
	seq      uint32
	total    uint32
	count    uint16
	buf      []byte
	received []bool
	got      uint16
}

// maxReassembleSize caps the reassembled payload to guard against a corrupt
// totalLen forcing a huge allocation (full grid is ~120KB; 4MB is generous).
const maxReassembleSize = 4 << 20

// Add feeds one datagram. When it completes a frame it returns the reassembled
// payload and true; otherwise it returns nil, false. A non-wire datagram or a
// malformed envelope returns an error (and false).
func (r *Reassembler) Add(datagram []byte) ([]byte, bool, error) {
	e, chunk, err := decodeEnvelope(datagram)
	if err != nil {
		return nil, false, err
	}
	if e.version != ProtocolVersion {
		return nil, false, fmt.Errorf("unsupported wire version %d", e.version)
	}
	if e.totalLen > maxReassembleSize {
		return nil, false, fmt.Errorf("implausible frame size %d", e.totalLen)
	}
	if e.chunkCount == 0 || e.chunkIndex >= e.chunkCount {
		return nil, false, fmt.Errorf("bad chunk index %d/%d", e.chunkIndex, e.chunkCount)
	}
	if int(e.chunkOffset)+len(chunk) > int(e.totalLen) {
		return nil, false, fmt.Errorf("chunk overruns payload")
	}

	// Start (or restart) a frame when the seq changes or nothing is in flight.
	if !r.have || e.frameSeq != r.seq {
		r.have = true
		r.seq = e.frameSeq
		r.total = e.totalLen
		r.count = e.chunkCount
		r.buf = make([]byte, e.totalLen)
		r.received = make([]bool, e.chunkCount)
		r.got = 0
	}

	if e.chunkCount != r.count || e.totalLen != r.total {
		return nil, false, fmt.Errorf("inconsistent chunk metadata for frame %d", e.frameSeq)
	}
	if r.received[e.chunkIndex] {
		return nil, false, nil // duplicate
	}
	copy(r.buf[e.chunkOffset:], chunk)
	r.received[e.chunkIndex] = true
	r.got++

	if r.got != r.count {
		return nil, false, nil
	}
	payload := r.buf
	r.have = false
	r.buf = nil
	r.received = nil
	return payload, true, nil
}
