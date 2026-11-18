//go:build !linux

// armQuickAck's no-op for the non-Linux builds: Linux's delayed-ACK socket
// option does not exist here, and the connection loop calls this per request,
// so the stub must stay a true no-op.
package main

import "net"

func armQuickAck(conn net.Conn) {}
