package recording

import (
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	FileExt = ".fh6rec.gz"

	magic   = "THR1"
	version = uint16(1)
)

type Manager struct {
	mu      sync.Mutex
	dir     string
	active  *Recorder
	records uint64
}

type Recorder struct {
	file    *os.File
	gzip    *gzip.Writer
	started time.Time
	last    time.Time
	path    string
	records uint64
}

type Info struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Status struct {
	Active    bool      `json:"active"`
	Name      string    `json:"name,omitempty"`
	Path      string    `json:"path,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Records   uint64    `json:"records"`
}

type Sample struct {
	OffsetMS uint64 `json:"offset_ms"`
	Packet   []byte `json:"-"`
}

func NewManager(dir string) (*Manager, error) {
	if dir == "" {
		dir = "recordings"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{dir: dir}, nil
}

func (m *Manager) Start() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active != nil {
		return m.statusLocked(), fmt.Errorf("recording already active")
	}

	now := time.Now()
	name := fmt.Sprintf("fh6-%s%s", now.Format("20060102-150405.000000000"), FileExt)
	path := filepath.Join(m.dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Status{}, err
	}

	gz := gzip.NewWriter(file)
	if _, err := gz.Write([]byte(magic)); err != nil {
		file.Close()
		return Status{}, err
	}
	if err := binary.Write(gz, binary.LittleEndian, version); err != nil {
		file.Close()
		return Status{}, err
	}

	m.active = &Recorder{
		file:    file,
		gzip:    gz,
		started: now,
		last:    now,
		path:    path,
	}
	m.records = 0
	return m.statusLocked(), nil
}

func (m *Manager) Stop() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active == nil {
		return m.statusLocked(), nil
	}

	status := Status{
		Active:  false,
		Name:    filepath.Base(m.active.path),
		Path:    m.active.path,
		Records: m.active.records,
	}
	err := m.active.close()
	m.active = nil
	m.records = 0
	return status, err
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) Record(packet []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(packet) > 65535 {
		return fmt.Errorf("packet too large: %d bytes", len(packet))
	}

	delta := at.Sub(m.active.last)
	if delta < 0 {
		delta = 0
	}
	m.active.last = at

	if err := binary.Write(m.active.gzip, binary.LittleEndian, uint64(delta/time.Microsecond)); err != nil {
		return err
	}
	if err := binary.Write(m.active.gzip, binary.LittleEndian, uint16(len(packet))); err != nil {
		return err
	}
	if _, err := m.active.gzip.Write(packet); err != nil {
		return err
	}
	m.active.records++
	m.records = m.active.records
	return nil
}

func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}

	infos := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileExt) {
			continue
		}
		stat, err := entry.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, Info{
			Name:     entry.Name(),
			Path:     filepath.Join(m.dir, entry.Name()),
			Size:     stat.Size(),
			Modified: stat.ModTime(),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Modified.After(infos[j].Modified)
	})
	return infos, nil
}

func (m *Manager) Read(name string, maxSamples int) ([]Sample, error) {
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return nil, fmt.Errorf("invalid recording name")
	}
	path := filepath.Join(m.dir, name)
	return ReadFile(path, maxSamples)
}

func (m *Manager) statusLocked() Status {
	if m.active == nil {
		return Status{}
	}
	return Status{
		Active:    true,
		Name:      filepath.Base(m.active.path),
		Path:      m.active.path,
		StartedAt: m.active.started,
		Records:   m.active.records,
	}
}

func (r *Recorder) close() error {
	gzipErr := r.gzip.Close()
	fileErr := r.file.Close()
	if gzipErr != nil {
		return gzipErr
	}
	return fileErr
}

func ReadFile(path string, maxSamples int) ([]Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	header := make([]byte, len(magic))
	if _, err := io.ReadFull(gz, header); err != nil {
		return nil, err
	}
	if string(header) != magic {
		return nil, fmt.Errorf("invalid recording magic")
	}

	var fileVersion uint16
	if err := binary.Read(gz, binary.LittleEndian, &fileVersion); err != nil {
		return nil, err
	}
	if fileVersion != version {
		return nil, fmt.Errorf("unsupported recording version %d", fileVersion)
	}

	samples := make([]Sample, 0)
	var offsetUS uint64
	for maxSamples <= 0 || len(samples) < maxSamples {
		var deltaUS uint64
		if err := binary.Read(gz, binary.LittleEndian, &deltaUS); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		var packetLen uint16
		if err := binary.Read(gz, binary.LittleEndian, &packetLen); err != nil {
			return nil, err
		}

		packet := make([]byte, packetLen)
		if _, err := io.ReadFull(gz, packet); err != nil {
			return nil, err
		}
		offsetUS += deltaUS
		samples = append(samples, Sample{
			OffsetMS: offsetUS / 1000,
			Packet:   packet,
		})
	}
	return samples, nil
}
