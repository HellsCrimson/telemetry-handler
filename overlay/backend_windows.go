//go:build windows

package overlay

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"telemetry-handler/config"
)

const (
	wsExTopmost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExLayered     = 0x00080000
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	wsPopup         = 0x80000000

	cwUseDefault = ^uintptr(0)

	swShowNoActivate = 4

	pmRemove = 0x0001

	ulwAlpha = 0x00000002

	acSrcOver  = 0x00
	acSrcAlpha = 0x01

	biRGB        = 0
	dibRGBColors = 0
	wmDestroy    = 0x0002
	wmClose      = 0x0010
	className    = "TelemetryHandlerOverlayWindow"
	windowTitle  = "Telemetry Handler Overlay"
	defaultDPI   = 96
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

type windowsBackend struct{}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type point struct {
	X int32
	Y int32
}

type size struct {
	CX int32
	CY int32
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

type windowsMonitor struct {
	Handle uintptr
	Device string
	Rect   rect
}

type overlayWindow struct {
	hwnd          uintptr
	monitor       windowsMonitor
	cfg           config.Overlay
	steeringWheel *SteeringWheel
}

func newBackend() Backend {
	return windowsBackend{}
}

func (windowsBackend) Run(ctx context.Context, cfg config.Overlay, updates <-chan HUD) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	monitors, err := enumMonitors()
	if err != nil {
		return err
	}
	monitor, err := selectMonitor(monitors, cfg.Output)
	if err != nil {
		return err
	}

	win, err := createOverlayWindow(cfg, monitor)
	if err != nil {
		return err
	}
	defer win.destroy()

	for {
		pumpMessages()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case hud := <-updates:
			if err := win.render(hud); err != nil {
				return err
			}
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func createOverlayWindow(cfg config.Overlay, monitor windowsMonitor) (*overlayWindow, error) {
	instance, _, err := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return nil, fmt.Errorf("GetModuleHandleW: %w", err)
	}

	classNamePtr, err := syscall.UTF16PtrFromString(className)
	if err != nil {
		return nil, err
	}
	windowTitlePtr, err := syscall.UTF16PtrFromString(windowTitle)
	if err != nil {
		return nil, err
	}

	wndProc := syscall.NewCallback(windowProc)
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   wndProc,
		Instance:  instance,
		ClassName: classNamePtr,
	}
	atom, _, regErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return nil, fmt.Errorf("RegisterClassExW: %w", regErr)
	}

	hwnd, _, createErr := procCreateWindowExW.Call(
		wsExTopmost|wsExLayered|wsExTransparent|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(windowTitlePtr)),
		wsPopup,
		cwUseDefault,
		cwUseDefault,
		uintptr(cfg.WidthValue()),
		uintptr(cfg.HeightValue()),
		0,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowExW: %w", createErr)
	}
	procShowWindow.Call(hwnd, swShowNoActivate)

	var steeringWheel *SteeringWheel
	if cfg.ShowSteering {
		if cfg.SteeringImagePath != "" {
			sw, err := LoadSteeringWheel(cfg.SteeringImagePath, cfg.SteeringSizeValue())
			if err != nil {
				log.Printf("failed to load steering wheel image: %v, using default", err)
				steeringWheel = NewSteeringWheel(cfg.SteeringSizeValue())
			} else {
				steeringWheel = sw
			}
		} else {
			steeringWheel = NewSteeringWheel(cfg.SteeringSizeValue())
		}
	}

	win := &overlayWindow{hwnd: hwnd, monitor: monitor, cfg: cfg, steeringWheel: steeringWheel}
	return win, nil
}

func (w *overlayWindow) render(hud HUD) error {
	width := w.cfg.WidthValue()
	height := w.cfg.HeightValue()
	x, y := placement(w.monitor.Rect, w.cfg)

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		return fmt.Errorf("GetDC: %w", err)
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return fmt.Errorf("CreateCompatibleDC: %w", err)
	}
	defer procDeleteDC.Call(memDC)

	info := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(width),
			Height:      -int32(height),
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
			SizeImage:   uint32(width * height * 4),
		},
	}

	var bits uintptr
	bitmap, _, err := procCreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 {
		return fmt.Errorf("CreateDIBSection: %w", err)
	}
	defer procDeleteObject.Call(bitmap)

	old, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, old)

	pixels := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), width*height)
	drawHUD(pixels, width, height, w.cfg.Opacity, hud)

	if w.steeringWheel != nil {
		steeringSize := w.cfg.SteeringSizeValue()
		steeringX := width - steeringSize - 64
		steeringY := 8
		steeringPixels := w.steeringWheel.GetRotated(hud.SteeringDegrees)
		drawSteering(pixels, width, height, steeringX, steeringY, steeringSize, w.cfg.Opacity, steeringPixels)
	}

	dst := point{X: int32(x), Y: int32(y)}
	src := point{}
	sz := size{CX: int32(width), CY: int32(height)}
	blend := blendFunction{BlendOp: acSrcOver, SourceConstantAlpha: 255, AlphaFormat: acSrcAlpha}

	ok, _, err := procUpdateLayeredWindow.Call(
		w.hwnd,
		screenDC,
		uintptr(unsafe.Pointer(&dst)),
		uintptr(unsafe.Pointer(&sz)),
		memDC,
		uintptr(unsafe.Pointer(&src)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
	if ok == 0 {
		return fmt.Errorf("UpdateLayeredWindow: %w", err)
	}
	return nil
}

func (w *overlayWindow) destroy() {
	if w.hwnd != 0 {
		procDestroyWindow.Call(w.hwnd)
		w.hwnd = 0
	}
}

func placement(monitor rect, cfg config.Overlay) (int, int) {
	width := cfg.WidthValue()
	height := cfg.HeightValue()

	left := int(monitor.Left)
	top := int(monitor.Top)
	right := int(monitor.Right)
	bottom := int(monitor.Bottom)

	x := left + cfg.MarginLeftValue()
	y := top + cfg.MarginTopValue()

	switch cfg.Anchor {
	case "top-right":
		x = right - width - cfg.MarginRightValue()
		y = top + cfg.MarginTopValue()
	case "bottom-left":
		x = left + cfg.MarginLeftValue()
		y = bottom - height - cfg.MarginBottomValue()
	case "bottom-right":
		x = right - width - cfg.MarginRightValue()
		y = bottom - height - cfg.MarginBottomValue()
	case "top":
		x = left + (right-left-width)/2
		y = top + cfg.MarginTopValue()
	case "bottom":
		x = left + (right-left-width)/2
		y = bottom - height - cfg.MarginBottomValue()
	}
	return x, y
}

func enumMonitors() ([]windowsMonitor, error) {
	var monitors []windowsMonitor
	callback := syscall.NewCallback(func(handle uintptr, _ uintptr, _ uintptr, data uintptr) uintptr {
		info := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		ok, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return 1
		}
		monitors = append(monitors, windowsMonitor{
			Handle: handle,
			Device: syscall.UTF16ToString(info.Device[:]),
			Rect:   info.Monitor,
		})
		return 1
	})

	ok, _, err := procEnumDisplayMonitors.Call(0, 0, callback, 0)
	if ok == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %w", err)
	}
	if len(monitors) == 0 {
		return nil, fmt.Errorf("Windows did not report any monitors")
	}
	return monitors, nil
}

func selectMonitor(monitors []windowsMonitor, output string) (windowsMonitor, error) {
	if output == "" {
		return monitors[0], nil
	}
	for i, monitor := range monitors {
		index := strconv.Itoa(i + 1)
		if monitor.Device == output || strings.EqualFold(monitor.Device, output) || index == output {
			return monitor, nil
		}
	}

	available := make([]string, 0, len(monitors))
	for i, monitor := range monitors {
		available = append(available, fmt.Sprintf("%s (%d)", monitor.Device, i+1))
	}
	return windowsMonitor{}, fmt.Errorf("overlay.output %q did not match any Windows monitor; available monitors: %s", output, strings.Join(available, ", "))
}

func pumpMessages() {
	var m msg
	for {
		ok, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove)
		if ok == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func windowProc(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	default:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	}
}
