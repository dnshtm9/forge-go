// debug.SetGCPercent(-1) at startup: the heap is a few MB and bounded.
package main

import (
	"runtime/debug"
)

func debugSetGCPercent() {
	debug.SetGCPercent(-1)
}
