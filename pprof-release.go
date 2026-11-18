//go:build !debug

// The shipped build's stubs: no net/http, no pprof, GC loosened to 400
// (the heap is small and bounded; 400 keeps it well under the pod's share).
package main

import (
	"runtime"
	"runtime/debug"
)

func debugSetGCPercent() {
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
