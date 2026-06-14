//go:build !windows

package main

import (
	"fmt"
	"runtime"
)

// mapping is a non-functional stub so the sidecar still compiles on the Linux
// dev box. The shared memory only exists inside the game's process namespace
// (the Wine prefix on Linux), so this binary must be built for Windows and run
// under that prefix — see the package doc comment.
type mapping struct{}

func openMapping(name string) (*mapping, error) {
	return nil, fmt.Errorf(
		"shared-memory telemetry is only available on Windows (built for %s); "+
			"build with GOOS=windows GOARCH=amd64 and run under the game's Wine prefix",
		runtime.GOOS)
}

func openMappingRW(name string) (*mapping, error) { return openMapping(name) }

func (m *mapping) snapshot(buf []byte)           {}
func (m *mapping) close()                        {}
func (m *mapping) readUint32(off int) uint32     { return 0 }
func (m *mapping) writeUint32(off int, v uint32) {}
