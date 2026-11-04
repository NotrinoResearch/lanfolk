package winnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type testSet map[netip.Addr]bool

func (s testSet) Contains(a netip.Addr) bool { return s[a] }

type pktOpts struct {
	src, dst         [4]byte
	proto            byte
	sport, dport     uint16
	payload          int
	ihlWords         int // 0 means 5
	flagsFrag        uint16
	versionOverride  byte
	totalLenOverride int
	trailing         int
}

func buildPacket(o pktOpts) []byte {
	ihl := 20
	if o.ihlWords > 5 {
		ihl = o.ihlWords * 4
	}
	udpLen := 8 + o.payload
	total := ihl + udpLen
	b := make([]byte, total+o.trailing)
	b[0] = 4<<4 | byte(ihl/4)
	if o.versionOverride != 0 {
		b[0] = o.versionOverride<<4 | byte(ihl/4)
	}
	tl := total
	if o.totalLenOverride != 0 {
		tl = o.totalLenOverride
	}
	binary.BigEndian.PutUint16(b[2:], uint16(tl))
	binary.BigEndian.PutUint16(b[6:], o.flagsFrag)
	b[8] = 128
	b[9] = o.proto
	copy(b[12:], o.src[:])
	copy(b[16:], o.dst[:])
	binary.BigEndian.PutUint16(b[ihl:], o.sport)
	binary.BigEndian.PutUint16(b[ihl+2:], o.dport)
	binary.BigEndian.PutUint16(b[ihl+4:], uint16(udpLen))
	return b
}

func validOpts() pktOpts {
	return pktOpts{
		src: [4]byte{192, 168, 1, 20}, dst: [4]byte{255, 255, 255, 255},
		proto: 17, sport: 54321, dport: 27015, payload: 12,
	}
}

func TestFilterBroadcast(t *testing.T) {
	local := testSet{
		netip.MustParseAddr("192.168.1.20"): true,
		netip.MustParseAddr("10.77.0.3"):    true, // the virtual adapter itself
		netip.MustParseAddr("127.0.0.1"):    true,
	}
	tests := []struct {
		name   string
		mod    func(*pktOpts)
		want   rejectReason
		packet func() []byte // overrides mod when set
	}{
		{name: "accepted", want: rejectNone},
		{name: "accepted with IP options", mod: func(o *pktOpts) { o.ihlWords = 7 }, want: rejectNone},
		{name: "accepted with link padding", mod: func(o *pktOpts) { o.trailing = 6 }, want: rejectNone},
		{name: "accepted empty payload", mod: func(o *pktOpts) { o.payload = 0 }, want: rejectNone},
		{name: "tcp", mod: func(o *pktOpts) { o.proto = 6 }, want: rejectNotUDP},
		{name: "icmp", mod: func(o *pktOpts) { o.proto = 1 }, want: rejectNotUDP},
		{name: "subnet broadcast", mod: func(o *pktOpts) { o.dst = [4]byte{192, 168, 1, 255} }, want: rejectNotBcast},
		{name: "virtual subnet broadcast", mod: func(o *pktOpts) { o.dst = [4]byte{10, 77, 0, 255} }, want: rejectNotBcast},
		{name: "multicast", mod: func(o *pktOpts) { o.dst = [4]byte{239, 255, 255, 250} }, want: rejectNotBcast},
		{name: "unicast to us", mod: func(o *pktOpts) { o.dst = [4]byte{192, 168, 1, 20} }, want: rejectNotBcast},
		{name: "foreign source", mod: func(o *pktOpts) { o.src = [4]byte{192, 168, 1, 99} }, want: rejectSourceNotMe},
		{name: "virtual source is not forwarded", mod: func(o *pktOpts) { o.src = [4]byte{10, 77, 0, 3} }, want: rejectVirtualSrc},
		{name: "virtual source of a peer", mod: func(o *pktOpts) { o.src = [4]byte{10, 77, 0, 9} }, want: rejectVirtualSrc},
		{name: "loopback source", mod: func(o *pktOpts) { o.src = [4]byte{127, 0, 0, 1} }, want: rejectSourceNotMe},
		{name: "unspecified source", mod: func(o *pktOpts) { o.src = [4]byte{} }, want: rejectSourceNotMe},
		{name: "same ports", mod: func(o *pktOpts) { o.sport, o.dport = 27015, 27015 }, want: rejectSamePorts},
		{name: "same ports 0", mod: func(o *pktOpts) { o.sport, o.dport = 0, 0 }, want: rejectSamePorts},
		{name: "more fragments", mod: func(o *pktOpts) { o.flagsFrag = 0x2000 }, want: rejectFragment},
		{name: "fragment offset", mod: func(o *pktOpts) { o.flagsFrag = 0x00b9 }, want: rejectFragment},
		{name: "dont fragment is fine", mod: func(o *pktOpts) { o.flagsFrag = 0x4000 }, want: rejectNone},
		{name: "ipv6 version", mod: func(o *pktOpts) { o.versionOverride = 6 }, want: rejectMalformed},
		{name: "total length beyond data", mod: func(o *pktOpts) { o.totalLenOverride = 500 }, want: rejectMalformed},
		{name: "total length too small", mod: func(o *pktOpts) { o.totalLenOverride = 24 }, want: rejectMalformed},
		{name: "empty", packet: func() []byte { return nil }, want: rejectMalformed},
		{name: "short", packet: func() []byte { return make([]byte, 19) }, want: rejectMalformed},
		{name: "ihl below 5", packet: func() []byte {
			b := buildPacket(validOpts())
			b[0] = 4<<4 | 4
			return b
		}, want: rejectMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pkt []byte
			if tc.packet != nil {
				pkt = tc.packet()
			} else {
				o := validOpts()
				if tc.mod != nil {
					tc.mod(&o)
				}
				pkt = buildPacket(o)
			}
			out, reason := filterBroadcast(pkt, local)
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
			if reason == rejectNone {
				ihl := 20
				if tc.mod != nil {
					o := validOpts()
					tc.mod(&o)
					if o.ihlWords > 5 {
						ihl = o.ihlWords * 4
					}
				}
				if len(out) != ihl+8+12 && tc.name != "accepted empty payload" {
					t.Errorf("returned %d bytes; padding not trimmed", len(out))
				}
				if int(binary.BigEndian.Uint16(out[2:4])) != len(out) {
					t.Errorf("returned length %d != IP total length", len(out))
				}
			} else if out != nil {
				t.Error("rejected packet returned data")
			}
		})
	}
}

func TestLocalAddrCache(t *testing.T) {
	now := time.Unix(1000, 0)
	listCalls := 0
	addrs := []netip.Addr{netip.MustParseAddr("192.168.1.20")}
	c := &localAddrCache{
		list:       func() ([]netip.Addr, error) { listCalls++; return addrs, nil },
		now:        func() time.Time { return now },
		minRefresh: 2 * time.Second,
	}
	a, b := netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("192.168.1.21")
	if !c.Contains(a) || listCalls != 1 {
		t.Fatalf("first lookup: calls=%d", listCalls)
	}
	if !c.Contains(a) || listCalls != 1 {
		t.Fatal("hit must not refresh")
	}
	if c.Contains(b) || listCalls != 1 {
		t.Fatal("miss within minRefresh must not refresh")
	}
	// A new DHCP lease appears.
	addrs = append(addrs, b)
	now = now.Add(3 * time.Second)
	if !c.Contains(b) || listCalls != 2 {
		t.Fatalf("miss after minRefresh must refresh: calls=%d", listCalls)
	}
}

func TestLocalAddrCacheListError(t *testing.T) {
	c := &localAddrCache{
		list:       func() ([]netip.Addr, error) { return nil, errors.New("boom") },
		now:        time.Now,
		minRefresh: time.Hour,
	}
	if c.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Error("contains after list error")
	}
}

func TestListLocalAddrs(t *testing.T) {
	addrs, err := listLocalAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if !a.Is4() {
			t.Errorf("non-IPv4 address %v", a)
		}
	}
}
