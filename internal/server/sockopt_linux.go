package server

import (
	"net"
	"syscall"
	"time"
)

const tcpUserTimeout = 18 // TCP_USER_TIMEOUT

// setUserTimeout bounds how long data we sent may stay unacknowledged
// before the connection is declared dead. Keep-alive only covers idle
// connections; this covers one that dies while replies are in flight.
func setUserTimeout(c *net.TCPConn, d time.Duration) {
	if raw, err := c.SyscallConn(); err == nil {
		raw.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, int(d/time.Millisecond))
		})
	}
}
