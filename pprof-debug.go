//go:build debug

// Debug-only pprof hook: built when the 'debug' tag is set
// (go build -tags debug -o forge-debug.exe .). The shipped binary compiles
// pprof-release.go's stubs instead.
package main

import (
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strconv"
)

// debugSetGCPercent keeps the default GC in the debug build: a live profile
// needs a collecting heap.
func debugSetGCPercent() {}

// debugStack answers the current goroutine's stack (the panic logger's).
func debugStack() []byte {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

// startPprofIfEnabled serves pprof on FORGE_PPROF's port when set.
func startPprofIfEnabled() {
	if port := os.Getenv("FORGE_PPROF"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			go func() {
				http.ListenAndServe("127.0.0.1:" + strconv.Itoa(p), nil)
			}()
		}
	}
}

func stopCPUProfile() {}
