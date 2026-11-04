//go:build windows

package transport

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// disableConnReset stops Windows from reporting ICMP port-unreachable as
// WSAECONNRESET on later reads, which would otherwise disturb the read loop
// whenever a peer candidate address is closed.
func disableConnReset(c *net.UDPConn) {
	rc, err := c.SyscallConn()
	if err != nil {
		return
	}
	const sioUDPConnReset = windows.IOC_IN | windows.IOC_VENDOR | 12
	rc.Control(func(fd uintptr) {
		flag := uint32(0)
		var ret uint32
		windows.WSAIoctl(windows.Handle(fd), sioUDPConnReset, (*byte)(unsafe.Pointer(&flag)),
			uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
	})
}
