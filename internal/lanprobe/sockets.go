package lanprobe

import (
	"context"
	"net"
	"net/netip"
	"syscall"
)

// listenUDP4 opens a UDP/IPv4 socket on addr. opts run on the raw socket
// before it is bound.
func listenUDP4(addr string, opts ...func(fd uintptr) error) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var optErr error
		if err := c.Control(func(fd uintptr) {
			for _, o := range opts {
				if optErr = o(fd); optErr != nil {
					return
				}
			}
		}); err != nil {
			return err
		}
		return optErr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// withMulticastIF returns a socket option that sets IP_MULTICAST_IF to the
// interface owning ip.
func withMulticastIF(ip netip.Addr) func(fd uintptr) error {
	return func(fd uintptr) error { return setMulticastIF(fd, ip.As4()) }
}

// joinOn joins group on the interface owning ifaceIP, for an open socket.
func joinOn(c *net.UDPConn, group, ifaceIP netip.Addr) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var jerr error
	if err := rc.Control(func(fd uintptr) { jerr = joinGroup(fd, group.As4(), ifaceIP.As4()) }); err != nil {
		return err
	}
	return jerr
}
