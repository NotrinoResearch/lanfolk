//go:build !windows

package transport

import "net"

func disableConnReset(*net.UDPConn) {}
