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
	procVirtualQuery    = kernel32.NewProc("VirtualQuery")
)

const fileMapRead = 0x0004

// memoryBasicInformation mirrors the Win32 MEMORY_BASIC_INFORMATION (x64
// layout) so VirtualQuery can report how large the mapped view is.
type memoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	PartitionId       uint16
	_                 uint16
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

// mapping holds an open view of a named Windows shared-memory section.
type mapping struct {
	handle uintptr
	view   uintptr // base address of the mapped view (non-Go memory, stable)
	size   uintptr // bytes mapped, so snapshot never reads past the section
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
	return &mapping{handle: h, view: view, size: viewSize(view)}, nil
}

// viewSize returns the byte size of the mapped region at addr (0 if unknown).
func viewSize(addr uintptr) uintptr {
	var mbi memoryBasicInformation
	ret, _, _ := procVirtualQuery.Call(addr, uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
	if ret == 0 {
		return 0
	}
	return mbi.RegionSize
}

// snapshot copies up to len(buf) bytes from the mapped view into buf, clamped to
// the mapped region so a buffer larger than the section never faults.
func (m *mapping) snapshot(buf []byte) {
	n := len(buf)
	if m.size > 0 && uintptr(n) > m.size {
		n = int(m.size)
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(m.view)), n)
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
