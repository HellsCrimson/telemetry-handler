//go:build linux

package overlay

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"telemetry-handler/config"
)

const (
	wlDisplayID  = 1
	wlRegistryID = 2

	wlShmFormatARGB8888 = 0

	layerOverlay = 3

	anchorTop    = 1
	anchorBottom = 2
	anchorLeft   = 4
	anchorRight  = 8
)

type waylandClient struct {
	conn       *net.UnixConn
	nextID     uint32
	compositor uint32
	shm        uint32
	layerShell uint32
	outputs    []waylandOutput
	pending    []byte
	debug      bool
}

type waylandOutput struct {
	globalName  uint32
	id          uint32
	name        string
	description string
}

type layerSurface struct {
	client        *waylandClient
	wlSurfaceID   uint32
	layerID       uint32
	inputRegionID uint32
	configured    bool
	lastSerial    uint32
	width         int
	height        int
}

type shmBuffer struct {
	file   *os.File
	data   []byte
	pixels []uint32
	width  int
	height int
	stride int
	size   int
	poolID uint32
	id     uint32
}

type wlMessage struct {
	objectID uint32
	opcode   uint16
	data     []byte
}

func connectWayland() (*waylandClient, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	socketPath := display
	if !filepath.IsAbs(socketPath) {
		socketPath = filepath.Join(runtimeDir, display)
	}

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("Wayland unavailable at %s: %w", socketPath, err)
	}

	client := &waylandClient{conn: conn, nextID: wlRegistryID, debug: os.Getenv("TELEMETRY_OVERLAY_WL_DEBUG") != ""}
	client.debugf("connected to %s", socketPath)
	if err := client.discoverGlobals(); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *waylandClient) Close() error {
	return c.conn.Close()
}

func (c *waylandClient) allocID() uint32 {
	c.nextID++
	return c.nextID
}

func (c *waylandClient) discoverGlobals() error {
	registryID := uint32(wlRegistryID)
	if err := c.send(wlDisplayID, 1, nil, registryID); err != nil {
		return err
	}
	callbackID := c.allocID()
	if err := c.send(wlDisplayID, 0, nil, callbackID); err != nil {
		return err
	}

	deadline := time.Now().Add(3 * time.Second)
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	defer c.conn.SetReadDeadline(time.Time{})
	for time.Now().Before(deadline) {
		msg, err := c.recv()
		if err != nil {
			return err
		}
		if err := c.handleDisplayError(msg); err != nil {
			return err
		}
		if msg.objectID == wlRegistryID && msg.opcode == 0 {
			if err := c.handleGlobal(msg.data); err != nil {
				return err
			}
		} else {
			c.handleOutputEvent(msg)
		}
		if msg.objectID == callbackID && msg.opcode == 0 {
			break
		}
	}

	if len(c.outputs) > 0 {
		if err := c.roundtrip(); err != nil {
			return err
		}
	}

	if c.compositor == 0 {
		return fmt.Errorf("Wayland compositor does not advertise wl_compositor")
	}
	if c.shm == 0 {
		return fmt.Errorf("Wayland compositor does not advertise wl_shm")
	}
	if c.layerShell == 0 {
		return fmt.Errorf("Wayland compositor does not support wlr-layer-shell")
	}
	return nil
}

func (c *waylandClient) handleGlobal(data []byte) error {
	r := newWLReader(data)
	name, ok := r.Uint32()
	if !ok {
		return fmt.Errorf("invalid wl_registry.global event")
	}
	iface, ok := r.String()
	if !ok {
		return fmt.Errorf("invalid wl_registry.global interface")
	}
	version, ok := r.Uint32()
	if !ok {
		return fmt.Errorf("invalid wl_registry.global version")
	}
	c.debugf("global name=%d interface=%s version=%d", name, iface, version)

	switch iface {
	case "wl_compositor":
		c.compositor = c.allocID()
		return c.bind(name, iface, min(version, 4), c.compositor)
	case "wl_shm":
		c.shm = c.allocID()
		return c.bind(name, iface, min(version, 1), c.shm)
	case "zwlr_layer_shell_v1":
		c.layerShell = c.allocID()
		return c.bind(name, iface, min(version, 4), c.layerShell)
	case "wl_output":
		id := c.allocID()
		c.outputs = append(c.outputs, waylandOutput{globalName: name, id: id})
		return c.bind(name, iface, min(version, 4), id)
	default:
		return nil
	}
}

func (c *waylandClient) bind(name uint32, iface string, version uint32, id uint32) error {
	c.debugf("bind name=%d interface=%s version=%d id=%d", name, iface, version, id)
	return c.send(wlRegistryID, 0, nil, name, iface, version, id)
}

func (c *waylandClient) roundtrip() error {
	callbackID := c.allocID()
	if err := c.send(wlDisplayID, 0, nil, callbackID); err != nil {
		return err
	}

	deadline := time.Now().Add(3 * time.Second)
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	defer c.conn.SetReadDeadline(time.Time{})

	for time.Now().Before(deadline) {
		msg, err := c.recv()
		if err != nil {
			return err
		}
		if err := c.handleDisplayError(msg); err != nil {
			return err
		}
		c.handleOutputEvent(msg)
		if msg.objectID == callbackID && msg.opcode == 0 {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for Wayland roundtrip")
}

func (c *waylandClient) CreateLayerSurface(cfg config.Overlay) (*layerSurface, error) {
	wlSurfaceID := c.allocID()
	if err := c.send(c.compositor, 0, nil, wlSurfaceID); err != nil {
		return nil, err
	}

	regionID := c.allocID()
	if err := c.send(c.compositor, 1, nil, regionID); err != nil {
		return nil, err
	}
	if err := c.send(wlSurfaceID, 5, nil, regionID); err != nil {
		return nil, err
	}

	layerID := c.allocID()
	outputID, err := c.selectOutputID(cfg.Output)
	if err != nil {
		return nil, err
	}
	if err := c.send(c.layerShell, 0, nil, layerID, wlSurfaceID, outputID, uint32(layerOverlay), "telemetry-handler"); err != nil {
		return nil, err
	}
	if err := c.send(layerID, 0, nil, uint32(cfg.WidthValue()), uint32(cfg.HeightValue())); err != nil {
		return nil, err
	}
	if err := c.send(layerID, 1, nil, anchorMask(cfg.Anchor)); err != nil {
		return nil, err
	}
	if err := c.send(layerID, 2, nil, int32(-1)); err != nil {
		return nil, err
	}
	if err := c.send(layerID, 3, nil, int32(cfg.MarginTopValue()), int32(cfg.MarginRightValue()), int32(cfg.MarginBottomValue()), int32(cfg.MarginLeftValue())); err != nil {
		return nil, err
	}
	if err := c.send(layerID, 4, nil, uint32(0)); err != nil {
		return nil, err
	}
	if err := c.send(wlSurfaceID, 6, nil); err != nil {
		return nil, err
	}

	surface := &layerSurface{
		client:        c,
		wlSurfaceID:   wlSurfaceID,
		layerID:       layerID,
		inputRegionID: regionID,
		width:         cfg.WidthValue(),
		height:        cfg.HeightValue(),
	}
	if err := surface.waitConfigure(); err != nil {
		return nil, err
	}
	return surface, nil
}

func (s *layerSurface) waitConfigure() error {
	deadline := time.Now().Add(3 * time.Second)
	if err := s.client.conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	defer s.client.conn.SetReadDeadline(time.Time{})
	for time.Now().Before(deadline) {
		msg, err := s.client.recv()
		if err != nil {
			return err
		}
		if err := s.client.handleDisplayError(msg); err != nil {
			return err
		}
		s.client.handleOutputEvent(msg)
		if msg.objectID == s.layerID && msg.opcode == 0 {
			r := newWLReader(msg.data)
			serial, ok := r.Uint32()
			if !ok {
				return fmt.Errorf("invalid layer surface configure event")
			}
			width, ok := r.Uint32()
			if ok && width > 0 {
				s.width = int(width)
			}
			height, ok := r.Uint32()
			if ok && height > 0 {
				s.height = int(height)
			}
			s.lastSerial = serial
			s.configured = true
			return s.client.send(s.layerID, 6, nil, serial)
		}
		if msg.objectID == s.layerID && msg.opcode == 1 {
			return fmt.Errorf("layer surface was closed by compositor")
		}
	}
	return fmt.Errorf("timed out waiting for layer surface configure")
}

func (c *waylandClient) selectOutputID(output string) (uint32, error) {
	if output == "" {
		return 0, nil
	}
	for _, candidate := range c.outputs {
		if outputMatches(candidate, output) {
			c.debugf("selected output name=%q description=%q global=%d id=%d", candidate.name, candidate.description, candidate.globalName, candidate.id)
			return candidate.id, nil
		}
	}

	available := make([]string, 0, len(c.outputs))
	for _, candidate := range c.outputs {
		label := candidate.name
		if label == "" {
			label = strconv.FormatUint(uint64(candidate.globalName), 10)
		}
		if candidate.description != "" {
			label += " (" + candidate.description + ")"
		}
		available = append(available, label)
	}
	if len(available) == 0 {
		return 0, fmt.Errorf("overlay.output %q requested, but compositor did not advertise any outputs", output)
	}
	return 0, fmt.Errorf("overlay.output %q did not match any Wayland output; available outputs: %s", output, strings.Join(available, ", "))
}

func outputMatches(candidate waylandOutput, output string) bool {
	if candidate.name == output || candidate.description == output || strconv.FormatUint(uint64(candidate.globalName), 10) == output {
		return true
	}
	return strings.EqualFold(candidate.name, output) || strings.EqualFold(candidate.description, output)
}

func (c *waylandClient) handleOutputEvent(msg wlMessage) {
	for i := range c.outputs {
		if c.outputs[i].id != msg.objectID {
			continue
		}
		r := newWLReader(msg.data)
		switch msg.opcode {
		case 4:
			name, ok := r.String()
			if ok {
				c.outputs[i].name = name
				c.debugf("output id=%d name=%q", msg.objectID, name)
			}
		case 5:
			description, ok := r.String()
			if ok {
				c.outputs[i].description = description
				c.debugf("output id=%d description=%q", msg.objectID, description)
			}
		}
		return
	}
}

func (s *layerSurface) CommitBuffer(buffer *shmBuffer) error {
	if !s.configured {
		if err := s.waitConfigure(); err != nil {
			return err
		}
	}
	if buffer.id == 0 {
		if err := s.client.createBuffer(buffer); err != nil {
			return err
		}
	}
	if err := s.client.send(s.wlSurfaceID, 1, nil, buffer.id, int32(0), int32(0)); err != nil {
		return err
	}
	if err := s.client.send(s.wlSurfaceID, 9, nil, int32(0), int32(0), int32(buffer.width), int32(buffer.height)); err != nil {
		return err
	}
	return s.client.send(s.wlSurfaceID, 6, nil)
}

func (s *layerSurface) Close() error {
	if s.client == nil {
		return nil
	}
	_ = s.client.send(s.layerID, 7, nil)
	_ = s.client.send(s.wlSurfaceID, 0, nil)
	_ = s.client.send(s.inputRegionID, 0, nil)
	return nil
}

func newSHMBuffer(width, height int) (*shmBuffer, error) {
	stride := width * 4
	size := stride * height
	file, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "telemetry-overlay-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(file.Name())
	if err := file.Truncate(int64(size)); err != nil {
		file.Close()
		return nil, err
	}
	data, err := syscall.Mmap(int(file.Fd()), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &shmBuffer{
		file:   file,
		data:   data,
		pixels: bytesToPixels(data),
		width:  width,
		height: height,
		stride: stride,
		size:   size,
	}, nil
}

func (c *waylandClient) createBuffer(buffer *shmBuffer) error {
	buffer.poolID = c.allocID()
	buffer.id = c.allocID()
	oob := syscall.UnixRights(int(buffer.file.Fd()))
	if err := c.send(c.shm, 0, oob, buffer.poolID, int32(buffer.size)); err != nil {
		return err
	}
	if err := c.send(buffer.poolID, 0, nil, buffer.id, int32(0), int32(buffer.width), int32(buffer.height), int32(buffer.stride), uint32(wlShmFormatARGB8888)); err != nil {
		return err
	}
	if err := c.send(buffer.poolID, 1, nil); err != nil {
		return err
	}
	return c.roundtrip()
}

func (b *shmBuffer) Close() error {
	if b.data != nil {
		_ = syscall.Munmap(b.data)
	}
	if b.file != nil {
		return b.file.Close()
	}
	return nil
}

func (c *waylandClient) handleDisplayError(msg wlMessage) error {
	if msg.objectID != wlDisplayID || msg.opcode != 0 {
		return nil
	}
	r := newWLReader(msg.data)
	objectID, _ := r.Uint32()
	code, _ := r.Uint32()
	text, _ := r.String()
	return fmt.Errorf("Wayland protocol error object=%d code=%d: %s", objectID, code, text)
}

func (c *waylandClient) send(objectID uint32, opcode uint16, oob []byte, args ...any) error {
	var body bytes.Buffer
	for _, arg := range args {
		switch v := arg.(type) {
		case uint32:
			_ = binary.Write(&body, binary.LittleEndian, v)
		case int32:
			_ = binary.Write(&body, binary.LittleEndian, uint32(v))
		case string:
			writeWLString(&body, v)
		default:
			return fmt.Errorf("unsupported Wayland argument %T", arg)
		}
	}

	size := uint32(8 + body.Len())
	var packet bytes.Buffer
	_ = binary.Write(&packet, binary.LittleEndian, objectID)
	_ = binary.Write(&packet, binary.LittleEndian, uint32(opcode)|(size<<16))
	packet.Write(body.Bytes())
	c.debugf("send %s object=%d opcode=%d size=%d fds=%t args=%d", requestName(objectID, opcode, c), objectID, opcode, size, len(oob) > 0, len(args))

	_, _, err := c.conn.WriteMsgUnix(packet.Bytes(), oob, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", requestName(objectID, opcode, c), err)
	}
	return nil
}

func (c *waylandClient) recv() (wlMessage, error) {
	for {
		if len(c.pending) >= 8 {
			header := binary.LittleEndian.Uint32(c.pending[4:8])
			size := int(header >> 16)
			if size < 8 {
				return wlMessage{}, fmt.Errorf("invalid Wayland message size %d", size)
			}
			if len(c.pending) >= size {
				packet := c.pending[:size]
				c.pending = c.pending[size:]
				msg := wlMessage{
					objectID: binary.LittleEndian.Uint32(packet[0:4]),
					opcode:   uint16(header & 0xffff),
					data:     append([]byte(nil), packet[8:size]...),
				}
				c.debugf("recv object=%d opcode=%d size=%d", msg.objectID, msg.opcode, size)
				return msg, nil
			}
		}

		buf := make([]byte, 64*1024)
		n, _, _, _, err := c.conn.ReadMsgUnix(buf, nil)
		if err != nil {
			return wlMessage{}, err
		}
		if n == 0 {
			return wlMessage{}, fmt.Errorf("Wayland connection closed")
		}
		c.pending = append(c.pending, buf[:n]...)
	}
}

func (c *waylandClient) debugf(format string, args ...any) {
	if c.debug {
		log.Printf("wayland: "+format, args...)
	}
}

func requestName(objectID uint32, opcode uint16, c *waylandClient) string {
	switch objectID {
	case wlDisplayID:
		switch opcode {
		case 0:
			return "wl_display.sync"
		case 1:
			return "wl_display.get_registry"
		}
	case wlRegistryID:
		if opcode == 0 {
			return "wl_registry.bind"
		}
	case c.compositor:
		switch opcode {
		case 0:
			return "wl_compositor.create_surface"
		case 1:
			return "wl_compositor.create_region"
		}
	case c.layerShell:
		if opcode == 0 {
			return "zwlr_layer_shell_v1.get_layer_surface"
		}
	case c.shm:
		if opcode == 0 {
			return "wl_shm.create_pool"
		}
	}
	return fmt.Sprintf("wayland object=%d opcode=%d", objectID, opcode)
}

type wlReader struct {
	data []byte
	pos  int
}

func newWLReader(data []byte) *wlReader {
	return &wlReader{data: data}
}

func (r *wlReader) Uint32() (uint32, bool) {
	if r.pos+4 > len(r.data) {
		return 0, false
	}
	value := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return value, true
}

func (r *wlReader) String() (string, bool) {
	n, ok := r.Uint32()
	if !ok || n == 0 || r.pos+int(n) > len(r.data) {
		return "", false
	}
	raw := r.data[r.pos : r.pos+int(n)]
	r.pos += paddedLen(int(n))
	if len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	return string(raw), true
}

func writeWLString(buf *bytes.Buffer, value string) {
	raw := append([]byte(value), 0)
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(raw)))
	buf.Write(raw)
	for i := len(raw); i < paddedLen(len(raw)); i++ {
		buf.WriteByte(0)
	}
}

func paddedLen(n int) int {
	return (n + 3) &^ 3
}

func anchorMask(anchor string) uint32 {
	switch anchor {
	case "top-left":
		return anchorTop | anchorLeft
	case "top-right":
		return anchorTop | anchorRight
	case "bottom-left":
		return anchorBottom | anchorLeft
	case "bottom-right":
		return anchorBottom | anchorRight
	case "top":
		return anchorTop
	case "bottom":
		return anchorBottom
	default:
		return anchorTop | anchorRight
	}
}

func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
