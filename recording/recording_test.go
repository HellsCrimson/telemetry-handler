package recording

import (
	"bytes"
	"testing"
	"time"
)

func TestRecordAndRead(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	status, err := manager.Start()
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	start := time.Unix(100, 0)
	first := bytes.Repeat([]byte{1}, 324)
	second := bytes.Repeat([]byte{2}, 324)
	if err := manager.Record(first, start); err != nil {
		t.Fatalf("Record first returned error: %v", err)
	}
	if err := manager.Record(second, start.Add(25*time.Millisecond)); err != nil {
		t.Fatalf("Record second returned error: %v", err)
	}
	if _, err := manager.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	samples, err := manager.Read(status.Name, 0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("len(samples) = %d, want 2", len(samples))
	}
	if samples[0].OffsetMS != 0 || samples[1].OffsetMS != 25 {
		t.Fatalf("offsets = %d/%d, want 0/25", samples[0].OffsetMS, samples[1].OffsetMS)
	}
	if !bytes.Equal(samples[0].Packet, first) || !bytes.Equal(samples[1].Packet, second) {
		t.Fatal("packet payload mismatch")
	}
}

func TestListRecordings(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	if _, err := manager.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if _, err := manager.Stop(); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	infos, err := manager.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if infos[0].Name == "" || infos[0].Size == 0 {
		t.Fatalf("unexpected info: %+v", infos[0])
	}
}
