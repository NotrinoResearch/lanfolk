// Package transport owns Lanfolk's single UDP socket, the custom WireGuard
// conn.Bind that sends through the path manager, and the shared relay
// client with its TCP fallback.
package transport

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Socket is the one UDP socket shared by STUN, probes, relay traffic and
// WireGuard, so that a hole-punched mapping is the same one the tunnel uses.
type Socket struct {
	conn   *net.UDPConn
	port   uint16
	filter atomic.Pointer[func(netip.AddrPort) bool]
}

// ListenSocket binds a dual-stack UDP socket. Use ":0" for a random port.
func ListenSocket(addr string) (*Socket, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	disableConnReset(c)
	// Small buffers: a packet that waits behind many others is already late.
	c.SetReadBuffer(512 * 1024)
	c.SetWriteBuffer(512 * 1024)
	return &Socket{conn: c, port: uint16(c.LocalAddr().(*net.UDPAddr).Port)}, nil
}

// Port returns the bound local port.
func (s *Socket) Port() uint16 { return s.port }

// LocalAddr returns the bound address.
func (s *Socket) LocalAddr() netip.AddrPort { return s.conn.LocalAddr().(*net.UDPAddr).AddrPort() }

// SetFilter installs a test hook that drops traffic to and from addresses
// for which f returns true. nil removes the filter.
func (s *Socket) SetFilter(f func(netip.AddrPort) bool) {
	if f == nil {
		s.filter.Store(nil)
		return
	}
	s.filter.Store(&f)
}

func (s *Socket) blocked(a netip.AddrPort) bool {
	if f := s.filter.Load(); f != nil {
		return (*f)(a)
	}
	return false
}

// WriteTo sends one datagram.
func (s *Socket) WriteTo(b []byte, addr netip.AddrPort) error {
	if s.blocked(addr) {
		return nil
	}
	_, err := s.conn.WriteToUDPAddrPort(b, addr)
	return err
}

// Run reads datagrams until the socket is closed. handler must not retain
// the slice after it returns.
func (s *Socket) Run(handler func(b []byte, from netip.AddrPort)) error {
	buf := make([]byte, wire.MaxPacket+64)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			continue // ICMP errors and similar transient failures
		}
		if n > wire.MaxPacket {
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if s.blocked(from) {
			continue
		}
		handler(buf[:n], from)
	}
}

// Close closes the socket.
func (s *Socket) Close() error { return s.conn.Close() }
