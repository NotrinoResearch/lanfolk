//go:build windows

package winnet

import (
	"net"
	"testing"
)

// The test process runs on the desktop, so its sockets are not services;
// RPC (TCP 135) is owned by a service on every Windows machine.
func TestLoadServicePorts(t *testing.T) {
	tl, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Close()
	ul, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	m, err := loadServicePorts()
	if err != nil {
		t.Fatal(err)
	}
	if m[serviceKey(ProtoTCP, uint16(tl.Addr().(*net.TCPAddr).Port))] {
		t.Error("a desktop program's TCP listener counts as a service")
	}
	if m[serviceKey(ProtoUDP, uint16(ul.LocalAddr().(*net.UDPAddr).Port))] {
		t.Error("a desktop program's UDP socket counts as a service")
	}
	if !m[serviceKey(ProtoTCP, 135)] {
		t.Errorf("TCP 135 (RPC) is not reported as a service port; table has %d entries", len(m))
	}
	f := NewServiceFilter()
	if !f.ServicePort(ProtoTCP, 135) {
		t.Error("filter does not report TCP 135")
	}
}
