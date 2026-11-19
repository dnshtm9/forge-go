//go:build !debug

// The shipped build's stubs: no net/http, no pprof, GC loosened to 400
// (the heap is small and bounded; 400 keeps it well under the pod's share).
// FORGE_GOGC overrides it for measurement: "off" disables GC entirely (the
// heap is bounded, so a trial cannot OOM through it), a number sets that
// percent. GOMEMLIMIT stays the runtime's own env knob.
package main

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

func debugSetGCPercent() {
	raw := strings.TrimSpace(os.Getenv("FORGE_GOGC"))
	if raw == "" {
		debug.SetGCPercent(400)
		return
	}
	if raw == "off" {
		debug.SetGCPercent(-1)
		return
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		debug.SetGCPercent(n)
		return
	}
	debug.SetGCPercent(400)
}

// debugStack answers the current goroutine's stack (the panic logger's).
func debugStack() []byte {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

func startPprofIfEnabled() {}
func stopCPUProfile()      {}
