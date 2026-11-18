//go:build linux

// TCP_QUICKACK: the peer's delayed-ACK timer is the cost of a loopback round
// trip's request/response pair (the server's ACK of a request can be held up
// to 40 ms waiting for a second segment that never comes). It is Linux-only
// and not persistent: the option resets after one ACK, so it is re-armed
// before every reply. Allocation-free; errors ignored (the option is best
// effort by nature).
//
// Each arm is one setsockopt through whatever the sandbox does to syscalls,
// and a lockstep request/response exchange always piggybacks the ACK on the
// reply anyway — so the default is OFF. FORGE_QUICKACK=1 arms it per read
// (the pre-2026-11-19 behaviour) when an experiment wants it back.
package main

import (
	"net"
	"os"
	"syscall"
)

// quickackEnabled is read once at startup: FORGE_QUICKACK set and not '0'.
var quickackEnabled = func() bool {
	v := os.Getenv("FORGE_QUICKACK")
	return v != "" && v != "0"
}()

// armQuickAck re-arms TCP_QUICKACK on the connection this request arrived on,
// so the reply's path leaves the request ACKed at once. A no-op unless
// FORGE_QUICKACK opts in.
func armQuickAck(conn net.Conn) {
	if !quickackEnabled {
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if raw, err := tcp.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
			})
		}
	}
}
