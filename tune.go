// debug.SetGCPercent(400) at startup: the heap is a few MB and bounded.
package main

import (
	"runtime"
	"runtime/debug"
)

func debugStack() []byte {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

func debugSetGCPercent() {
	debug.SetGCPercent(400)
}
