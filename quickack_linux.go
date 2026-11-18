//go:build linux

// TCP_QUICKACK: the peer's delayed-ACK timer is the cost of a loopback round
// trip's request/response pair (the server's ACK of a request can be held up
// to 40 ms waiting for a second segment that never comes). It is Linux-only
// and not persistent: the option resets after one ACK, so it is re-armed
// before every reply. Allocation-free; errors ignored (the option is best
// effort by nature).
package main

import (
	"net"
	"syscall"
)

// armQuickAck re-arms TCP_QUICKACK on the connection this request arrived on,
// so the reply's path leaves the request ACKed at once.
func armQuickAck(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		if raw, err := tcp.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_QUICKACK, 1)
			})
		}
	}
}
