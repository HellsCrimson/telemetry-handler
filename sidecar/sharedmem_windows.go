//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procOpenFileMapping = kernel32.NewProc("OpenFileMappingW")
	procMapViewOfFile   = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile = kernel32.NewProc("UnmapViewOfFile")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
)

const fileMapRead = 0x0004

// mapping holds an open view of a named Windows shared-memory section.
type mapping struct {
	handle uintptr
	view   uintptr // base address of the mapped view (non-Go memory, stable)
}

func openMapping(name string) (*mapping, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, _, callErr := procOpenFileMapping.Call(uintptr(fileMapRead), 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return nil, fmt.Errorf("OpenFileMapping(%q): %v (game running with the rF2 shared-memory plugin enabled?)", name, callErr)
	}
	view, _, callErr := procMapViewOfFile.Call(h, uintptr(fileMapRead), 0, 0, 0)
	if view == 0 {
		procCloseHandle.Call(h)
		return nil, fmt.Errorf("MapViewOfFile(%q): %v", name, callErr)
	}
	return &mapping{handle: h, view: view}, nil
}

// snapshot copies len(buf) bytes from the mapped view into buf.
func (m *mapping) snapshot(buf []byte) {
	src := unsafe.Slice((*byte)(unsafe.Pointer(m.view)), len(buf))
	copy(buf, src)
}

func (m *mapping) close() {
	if m.view != 0 {
		procUnmapViewOfFile.Call(m.view)
	}
	if m.handle != 0 {
		procCloseHandle.Call(m.handle)
	}
}
