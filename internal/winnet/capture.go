package winnet

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

// ErrNotElevated is returned by StartBroadcastCapture when the process has no
// administrator rights. Raw sockets need them.
var ErrNotElevated = errors.New("winnet: broadcast capture needs administrator rights (raw socket)")

// Global broadcast capture (Windows only)
//
// Windows sends a datagram addressed to 255.255.255.255 through one interface
// only, normally the physical adapter, so the virtual adapter never sees game
// discovery broadcasts. Windows also delivers a copy of every locally sent
// broadcast to the local host over the loopback path. A raw IPv4 socket bound
// to 127.0.0.1 with SIO_RCVALL receives that copy together with its IP header.
// The service forwards the captured packets to its peers.
//
// Known limitation: the engine re-injects packets from peers into the adapter,
// and a captured packet whose source port equals its destination port looks
// the same as such a re-injected broadcast. To rule out forwarding loops
// those packets are skipped. Games that broadcast from the port they listen on
// (source port == destination port) are therefore not visible in capture mode
// and need compatibility mode instead. IP fragments are skipped too, because
// only the first fragment carries the UDP header.

// Reasons why a captured packet is rejected.
type rejectReason string

const (
	rejectNone        rejectReason = ""
	rejectMalformed   rejectReason = "malformed IPv4 header"
	rejectNotUDP      rejectReason = "not UDP"
	rejectFragment    rejectReason = "IP fragment"
	rejectNotBcast    rejectReason = "destination is not 255.255.255.255"
	rejectSourceNotMe rejectReason = "source is not a local address"
	rejectVirtualSrc  rejectReason = "source is in the virtual network"
	rejectSamePorts   rejectReason = "source and destination ports are identical"
)

// addrSet answers whether an address belongs to this machine.
type addrSet interface {
	Contains(netip.Addr) bool
}

// filterBroadcast decides whether a captured IPv4 packet is a locally sent
// global broadcast that should be forwarded. It accepts only unfragmented UDP
// packets to 255.255.255.255 from a local, non-loopback address outside the
// virtual network whose source and destination ports differ. On success it
// returns the packet trimmed to its IP total length.
func filterBroadcast(pkt []byte, local addrSet) ([]byte, rejectReason) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return nil, rejectMalformed
	}
	ihl := int(pkt[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(pkt[2:4]))
	if ihl < 20 || total < ihl+8 || total > len(pkt) {
		return nil, rejectMalformed
	}
	if pkt[9] != ProtoUDP {
		return nil, rejectNotUDP
	}
	// More-fragments flag or a non-zero fragment offset.
	if binary.BigEndian.Uint16(pkt[6:8])&0x3fff != 0 {
		return nil, rejectFragment
	}
	dst := netip.AddrFrom4([4]byte(pkt[16:20]))
	if dst != netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, rejectNotBcast
	}
	src := netip.AddrFrom4([4]byte(pkt[12:16]))
	if VirtualNetwork.Contains(src) {
		return nil, rejectVirtualSrc
	}
	if src.IsLoopback() || src.IsUnspecified() || !local.Contains(src) {
		return nil, rejectSourceNotMe
	}
	udp := pkt[ihl:total]
	if binary.BigEndian.Uint16(udp[0:2]) == binary.BigEndian.Uint16(udp[2:4]) {
		return nil, rejectSamePorts
	}
	return pkt[:total], rejectNone
}

// localAddrCache is a periodically refreshed set of this machine's IPv4
// addresses. A lookup miss triggers one refresh, at most once per minRefresh,
// so a new DHCP lease is noticed without scanning the interfaces for every
// foreign packet on the loopback path.
type localAddrCache struct {
	list       func() ([]netip.Addr, error)
	now        func() time.Time
	minRefresh time.Duration

	mu      sync.Mutex
	set     map[netip.Addr]struct{}
	refresh time.Time
}

func newLocalAddrCache() *localAddrCache {
	return &localAddrCache{list: listLocalAddrs, now: time.Now, minRefresh: 2 * time.Second}
}

// Contains implements addrSet.
func (c *localAddrCache) Contains(a netip.Addr) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.set[a]; ok {
		return true
	}
	if c.set != nil && c.now().Sub(c.refresh) < c.minRefresh {
		return false
	}
	c.refreshLocked()
	_, ok := c.set[a]
	return ok
}

func (c *localAddrCache) refreshLocked() {
	c.refresh = c.now()
	addrs, err := c.list()
	if err != nil {
		if c.set == nil {
			c.set = map[netip.Addr]struct{}{}
		}
		return
	}
	set := make(map[netip.Addr]struct{}, len(addrs))
	for _, a := range addrs {
		set[a] = struct{}{}
	}
	c.set = set
}

// listLocalAddrs returns the IPv4 unicast addresses of all interfaces.
func listLocalAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Unmap().Is4() {
			out = append(out, ip.Unmap())
		}
	}
	return out, nil
}
