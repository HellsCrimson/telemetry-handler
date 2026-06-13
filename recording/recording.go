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

// Start begins a new recording. label is a short, filename-safe prefix
// identifying the telemetry source (e.g. "forza"/"lmu"); it is sanitized and
// defaults to "session" when empty. Recordings are source-agnostic byte streams
// regardless of label, so a recording of either game replays the same way.
func (m *Manager) Start(label string) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.active != nil {
		return m.statusLocked(), fmt.Errorf("recording already active")
	}

	now := time.Now()
	name := fmt.Sprintf("%s-%s%s", sanitizeLabel(label), now.Format("20060102-150405.000000000"), FileExt)
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

// sanitizeLabel reduces a source label to a safe filename prefix: lowercase
// alphanumerics only, falling back to "session" when nothing usable remains.
func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
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
