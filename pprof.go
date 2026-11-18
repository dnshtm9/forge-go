// Optional pprof listener for local profiling: active only when FORGE_PPROF is
// set. Inert in a trial (the variable is never set there).
package main

import (
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/pprof"
	"strconv"
)

// startPprofIfEnabled serves pprof on the named port and writes a CPU profile
// for the process's whole life to FORGE_CPUPROF's path when set.
func startPprofIfEnabled() {
	if port := os.Getenv("FORGE_PPROF"); port != "" {
		p, err := strconv.Atoi(port)
		if err == nil {
			go func() {
				http.ListenAndServe("127.0.0.1:" + strconv.Itoa(p), nil)
			}()
		}
	}
	if path := os.Getenv("FORGE_CPUPROF"); path != "" {
		f, err := os.Create(path)
		if err == nil {
			pprof.StartCPUProfile(f)
		}
	}
}

// stopCPUProfile flushes a started CPU profile (called at exit).
func stopCPUProfile() {
	pprof.StopCPUProfile()
}
