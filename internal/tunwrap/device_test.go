package tunwrap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeviceImplementsTun(t *testing.T) {
	e := newEnv(t, nil)
	if e.d.BatchSize() < 16 {
		t.Fatalf("BatchSize = %d, want >= 16", e.d.BatchSize())
	}
	if name, err := e.d.Name(); err != nil || name != "test0" {
		t.Fatalf("Name = %q, %v", name, err)
	}
	if mtu, err := e.d.MTU(); err != nil || mtu != 1380 {
		t.Fatalf("MTU = %d, %v", mtu, err)
	}
	if e.d.Events() == nil {
		t.Fatal("nil Events channel")
	}
}

func TestUnicastPassthroughOutbound(t *testing.T) {
	e := newEnv(t, nil, addrB)
	for _, pkt := range [][]byte{
		udpPkt(addrSelf, addrB, 5000, 27015, "game data"),
		rawTCP(addrSelf, addrB, 40000, 25565),
		rawIPv4(addrSelf, addrB, 1, 64, []byte{8, 0, 0, 0, 0, 1, 0, 1}), // ICMP echo
	} {
		e.mem.InjectFromOS(pkt)
		got := e.readOne(t)
		if !bytes.Equal(got, pkt) {
			t.Fatalf("unicast changed:\n got %x\nwant %x", got, pkt)
		}
	}
	if s := e.d.Stats(); s.UnicastOut != 3 {
		t.Fatalf("UnicastOut = %d, want 3", s.UnicastOut)
	}
}

func TestUnicastPassthroughInbound(t *testing.T) {
	e := newEnv(t, nil, addrB)
	pkts := [][]byte{
		udpPkt(addrB, addrSelf, 27015, 5000, "reply"),
		rawTCP(addrB, addrSelf, 25565, 40000),
	}
	e.writeIn(t, pkts...)
	got := e.drainOS()
	if len(got) != 2 {
		t.Fatalf("delivered %d packets, want 2", len(got))
	}
	for i := range pkts {
		if !bytes.Equal(got[i], pkts[i]) {
			t.Fatalf("packet %d changed:\n got %x\nwant %x", i, got[i], pkts[i])
		}
	}
	if s := e.d.Stats(); s.UnicastIn != 2 {
		t.Fatalf("UnicastIn = %d, want 2", s.UnicastIn)
	}
	// All accepted packets go to the inner device in one call.
	if calls := e.mem.WriteCalls(); calls != 1 {
		t.Fatalf("inner Write calls = %d, want 1", calls)
	}
}

func TestBlockedPortsOutbound(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 5000, 27015, "sentinel")
	blocked := [][]byte{
		rawTCP(addrSelf, addrB, 40000, 135),
		rawTCP(addrSelf, addrB, 40000, 139),
		rawTCP(addrSelf, addrB, 40000, 445),
		rawTCP(addrSelf, addrB, 40000, 3389),
		rawTCP(addrSelf, addrB, 40000, 5985),
		rawTCP(addrSelf, addrB, 40000, 5986),
		udpPkt(addrSelf, addrB, 5000, 137, "nb"),
		udpPkt(addrSelf, addrB, 5000, 138, "nb"),
	}
	for _, p := range blocked {
		e.mem.InjectFromOS(p)
	}
	e.mem.InjectFromOS(good)
	var got [][]byte
	for len(got) < 1 {
		got = append(got, e.readN(t, 16)...)
	}
	if len(got) != 1 || !bytes.Equal(got[0], good) {
		t.Fatalf("expected only the sentinel, got %d packets", len(got))
	}
	if s := e.d.Stats(); s.DroppedBlocked != uint64(len(blocked)) {
		t.Fatalf("DroppedBlocked = %d, want %d", s.DroppedBlocked, len(blocked))
	}
	// Ports next to the blocked ones are open.
	for _, p := range [][]byte{rawTCP(addrSelf, addrB, 1, 136), rawTCP(addrSelf, addrB, 1, 5987), udpPkt(addrSelf, addrB, 1, 139, "x")} {
		e.mem.InjectFromOS(p)
		if got := e.readOne(t); !bytes.Equal(got, p) {
			t.Fatal("open port was dropped")
		}
	}
}

func TestBlockedPortsInbound(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrB, addrSelf, 5000, 27015, "sentinel")
	blocked := [][]byte{
		rawTCP(addrB, addrSelf, 40000, 445),
		rawTCP(addrB, addrSelf, 40000, 3389),
		rawTCP(addrB, addrSelf, 40000, 5986),
		udpPkt(addrB, addrSelf, 5000, 137, "nb"),
	}
	e.writeIn(t, append(blocked, good)...)
	got := e.drainOS()
	if len(got) != 1 || !bytes.Equal(got[0], good) {
		t.Fatalf("expected only the sentinel, got %d packets", len(got))
	}
	if s := e.d.Stats(); s.DroppedBlocked != uint64(len(blocked)) {
		t.Fatalf("DroppedBlocked = %d, want %d", s.DroppedBlocked, len(blocked))
	}
}

// tcpFlags returns a copy of a rawTCP packet with other TCP flags.
func tcpFlags(pkt []byte, flags byte) []byte {
	p := append([]byte(nil), pkt...)
	p[20+13] = flags
	return p
}

// Ports owned only by system services are unreachable for members: UDP and
// new TCP connections. Replies to our own connections and outbound traffic
// are not affected.
func TestServicePortsInbound(t *testing.T) {
	asked := 0
	service := func(proto uint8, port uint16) bool {
		asked++
		return proto == 6 && port == 5432 || proto == 17 && port == 5353
	}
	e := newEnv(t, func(c *Config) { c.ServicePort = service }, addrB)
	dropped := [][]byte{
		rawTCP(addrB, addrSelf, 40000, 5432), // SYN
		udpPkt(addrB, addrSelf, 5000, 5353, "unicast"),
		lanFrame(addrB, addrSelf, udpPkt(addrB, netip.MustParseAddr("224.0.0.251"), 5353, 5353, "mdns")),
	}
	passed := [][]byte{
		tcpFlags(rawTCP(addrB, addrSelf, 5432, 40000), 0x12), // SYN-ACK: reply to our connection
		tcpFlags(rawTCP(addrB, addrSelf, 40000, 5432), 0x10), // ACK only: no new connection
		rawTCP(addrB, addrSelf, 40000, 25565),                // a game's TCP port
		udpPkt(addrB, addrSelf, 5000, 27015, "game"),
	}
	e.writeIn(t, append(dropped, passed...)...)
	got := e.drainOS()
	if len(got) != len(passed) {
		t.Fatalf("delivered %d packets, want %d", len(got), len(passed))
	}
	for i := range passed {
		if !bytes.Equal(got[i], passed[i]) {
			t.Errorf("packet %d changed or reordered", i)
		}
	}
	if s := e.d.Stats(); s.DroppedService != uint64(len(dropped)) {
		t.Fatalf("DroppedService = %d, want %d", s.DroppedService, len(dropped))
	}

	// Outbound traffic to a member's port is never asked about.
	before := asked
	out := rawTCP(addrSelf, addrB, 40000, 5432)
	e.mem.InjectFromOS(out)
	if p := e.readOne(t); !bytes.Equal(p, out) {
		t.Fatal("outbound packet changed")
	}
	if asked != before {
		t.Fatal("outbound packet was checked against local services")
	}
}

func TestNilBlockedAllowsEverything(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Blocked = nil }, addrB)
	p := rawTCP(addrSelf, addrB, 1, 445)
	e.mem.InjectFromOS(p)
	if got := e.readOne(t); !bytes.Equal(got, p) {
		t.Fatal("packet changed")
	}
}

func TestIPv6Dropped(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	e.mem.InjectFromOS(ipv6Pkt())
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
	e.writeIn(t, ipv6Pkt(), udpPkt(addrB, addrSelf, 2, 1, "in"))
	if got := e.drainOS(); len(got) != 1 {
		t.Fatalf("delivered %d packets, want 1", len(got))
	}
	if s := e.d.Stats(); s.DroppedIPv6 != 2 {
		t.Fatalf("DroppedIPv6 = %d, want 2", s.DroppedIPv6)
	}
}

func TestMalformedOutboundDropped(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	trunc := udpPkt(addrSelf, addrB, 1, 2, "payload")
	badLen := udpPkt(addrSelf, addrB, 1, 2, "payload")
	binary.BigEndian.PutUint16(badLen[2:], uint16(len(badLen)+10))
	badIHL := udpPkt(addrSelf, addrB, 1, 2, "payload")
	badIHL[0] = 0x44
	tinyTCP := rawIPv4(addrSelf, addrB, 6, 64, []byte{0, 1}) // no room for the ports
	for _, p := range [][]byte{nil, {0x45}, trunc[:21], badLen, badIHL, tinyTCP} {
		e.mem.InjectFromOS(p)
	}
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
	if s := e.d.Stats(); s.DroppedMalformed != 6 {
		t.Fatalf("DroppedMalformed = %d, want 6", s.DroppedMalformed)
	}
}

func TestSpoofedSourceDropped(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	e.mem.InjectFromOS(udpPkt(addrC, addrB, 1, 2, "spoof"))
	e.mem.InjectFromOS(udpPkt(netip.IPv4Unspecified(), addrB, 1, 2, "zero"))
	e.mem.InjectFromOS(udpPkt(netip.MustParseAddr("192.168.1.9"), addrB, 1, 2, "lan"))
	e.mem.InjectFromOS(udpPkt(addrC, addrBcast, 1, 2, "spoofed broadcast"))
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
	if s := e.d.Stats(); s.DroppedSpoofed != 4 {
		t.Fatalf("DroppedSpoofed = %d, want 4", s.DroppedSpoofed)
	}
}

func TestSelfFollowsActiveNetwork(t *testing.T) {
	e := newEnv(t, nil, addrB)
	newSelf := netip.MustParseAddr("10.77.0.9")
	e.setSelf(newSelf)
	e.mem.InjectFromOS(udpPkt(addrSelf, addrB, 1, 2, "old"))
	good := udpPkt(newSelf, addrB, 1, 2, "new")
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the packet from the new address")
	}
}

func TestUnicastRouting(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	e.mem.InjectFromOS(udpPkt(addrSelf, netip.MustParseAddr("8.8.8.8"), 1, 2, "internet"))
	e.mem.InjectFromOS(udpPkt(addrSelf, netip.MustParseAddr("192.168.1.1"), 1, 2, "lan"))
	e.mem.InjectFromOS(udpPkt(addrSelf, netip.MustParseAddr("10.77.0.0"), 1, 2, "network address"))
	e.mem.InjectFromOS(rawIPv4(addrSelf, addrB, LanFrameProto, 64, []byte("LF\x01\x00xxxxxxxx")))
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
	if s := e.d.Stats(); s.DroppedOther != 4 {
		t.Fatalf("DroppedOther = %d, want 4", s.DroppedOther)
	}
}

func TestInboundUnicastValidation(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrB, addrSelf, 1, 2, "sentinel")
	e.writeIn(t,
		udpPkt(addrB, addrC, 1, 2, "wrong destination"),
		udpPkt(netip.MustParseAddr("192.168.1.5"), addrSelf, 1, 2, "source outside prefix"),
		udpPkt(addrSelf, addrSelf, 1, 2, "source is self"),
		udpPkt(addrBcast, addrSelf, 1, 2, "source is broadcast"),
		good,
	)
	got := e.drainOS()
	if len(got) != 1 || !bytes.Equal(got[0], good) {
		t.Fatalf("expected only the sentinel, got %d packets", len(got))
	}
	if s := e.d.Stats(); s.DroppedSpoofed != 3 || s.DroppedOther != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestFanOutOutbound(t *testing.T) {
	for _, dst := range []netip.Addr{addrBcast, addrGlobal, addrMcast, netip.MustParseAddr("224.0.0.251")} {
		t.Run(dst.String(), func(t *testing.T) {
			e := newEnv(t, nil, addrB, addrC, addrD)
			pkt := udpPkt(addrSelf, dst, 4000, 4001, "who is there")
			e.mem.InjectFromOS(pkt)
			got := e.readN(t, 16)
			if len(got) != 3 {
				t.Fatalf("got %d frames, want 3", len(got))
			}
			for i, peer := range []netip.Addr{addrB, addrC, addrD} {
				checkFrame(t, got[i], addrSelf, peer, pkt)
			}
			if s := e.d.Stats(); s.LanFramesOut != 3 {
				t.Fatalf("LanFramesOut = %d, want 3", s.LanFramesOut)
			}
		})
	}
}

func TestFanOutWithoutPeers(t *testing.T) {
	e := newEnv(t, nil)
	e.mem.InjectFromOS(udpPkt(addrSelf, addrBcast, 1, 2, "nobody home"))
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
}

func TestFanOutSkipsInvalidPeers(t *testing.T) {
	e := newEnv(t, nil, addrSelf, netip.Addr{}, netip.MustParseAddr("192.168.1.1"), addrB)
	pkt := udpPkt(addrSelf, addrBcast, 1, 2, "x")
	e.mem.InjectFromOS(pkt)
	got := e.readN(t, 16)
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	checkFrame(t, got[0], addrSelf, addrB, pkt)
}

func TestBroadcastDropRules(t *testing.T) {
	e := newEnv(t, nil, addrB)
	good := udpPkt(addrSelf, addrB, 1, 2, "sentinel")
	frag := udpPkt(addrSelf, addrBcast, 1, 2, "fragment")
	frag[6], frag[7] = 0x20, 0x00 // more fragments
	binary.BigEndian.PutUint16(frag[10:], 0)
	binary.BigEndian.PutUint16(frag[10:], refChecksum(frag[:20]))
	for _, p := range [][]byte{
		rawIPv4(addrSelf, netip.MustParseAddr("224.0.0.22"), 2, 1, []byte{0x16, 0, 0, 0, 0, 0, 0, 0}), // IGMP
		rawTCP(addrSelf, addrBcast, 1, 2),
		rawIPv4(addrSelf, addrGlobal, 1, 64, []byte{8, 0, 0, 0, 0, 0, 0, 0}), // ICMP
		udpPkt(addrSelf, addrBcast, 1, 137, "netbios name query"),
		udpPkt(addrSelf, addrGlobal, 1, 138, "netbios datagram"),
		udpPkt(addrSelf, addrMcast, 1, 137, "netbios via multicast"),
		frag,
	} {
		e.mem.InjectFromOS(p)
	}
	e.mem.InjectFromOS(good)
	if got := e.readOne(t); !bytes.Equal(got, good) {
		t.Fatal("expected the sentinel only")
	}
	s := e.d.Stats()
	if s.DroppedBlocked != 3 || s.DroppedOther != 4 || s.LanFramesOut != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

// Name resolution and device discovery multicast (LLMNR, SSDP, WS-Discovery)
// must not reach other members in either direction; mDNS stays.
func TestDiscoveryMulticastNotForwarded(t *testing.T) {
	llmnr := netip.MustParseAddr("224.0.0.252")
	ssdp := netip.MustParseAddr("239.255.255.250")
	mdns := netip.MustParseAddr("224.0.0.251")

	e := newEnv(t, nil, addrB)
	for _, p := range [][]byte{
		udpPkt(addrSelf, llmnr, 50000, 5355, "llmnr query"),
		udpPkt(addrSelf, ssdp, 50000, 1900, "M-SEARCH"),
		udpPkt(addrSelf, ssdp, 50000, 3702, "ws-discovery probe"),
	} {
		e.mem.InjectFromOS(p)
	}
	keep := udpPkt(addrSelf, mdns, 5353, 5353, "mdns query")
	e.mem.InjectFromOS(keep)
	got := e.readN(t, 16)
	if len(got) != 1 {
		t.Fatalf("got %d frames, want only the mDNS one", len(got))
	}
	checkFrame(t, got[0], addrSelf, addrB, keep)
	if s := e.d.Stats(); s.DroppedBlocked != 3 {
		t.Fatalf("DroppedBlocked = %d, want 3", s.DroppedBlocked)
	}

	// Inbound: a member's LLMNR answer bait never reaches the adapter.
	in := newEnv(t, nil, addrB)
	in.writeIn(t, lanFrame(addrB, addrSelf, udpPkt(addrB, llmnr, 50000, 5355, "llmnr")))
	if pkts := in.drainOS(); len(pkts) != 0 {
		t.Fatalf("inbound LLMNR reached the adapter: %d packets", len(pkts))
	}
	if s := in.d.Stats(); s.DroppedBlocked != 1 {
		t.Fatalf("inbound DroppedBlocked = %d, want 1", s.DroppedBlocked)
	}
}

func TestReadWithFewerBufsThanCopies(t *testing.T) {
	peers := []netip.Addr{addrB, addrC, addrD, netip.MustParseAddr("10.77.0.6"), netip.MustParseAddr("10.77.0.7")}
	e := newEnv(t, nil, peers...)
	pkt := udpPkt(addrSelf, addrBcast, 1, 2, "x")
	uni := udpPkt(addrSelf, addrB, 1, 2, "unicast after broadcast")
	e.mem.InjectFromOS(pkt)
	e.mem.InjectFromOS(uni)
	// Five copies and the unicast packet that follows come out two at a time,
	// in order, and nothing is lost.
	var order [][]byte
	for _, want := range []int{2, 2, 2} {
		got := e.readN(t, 2)
		if len(got) != want {
			t.Fatalf("Read returned %d packets, want %d", len(got), want)
		}
		order = append(order, got...)
	}
	for i, peer := range peers {
		checkFrame(t, order[i], addrSelf, peer, pkt)
	}
	if !bytes.Equal(order[5], uni) {
		t.Fatal("unicast packet lost or reordered")
	}
	if s := e.d.Stats(); s.LanFramesOut != 5 || s.UnicastOut != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestReadDoesNotWaitForMorePackets(t *testing.T) {
	e := newEnv(t, nil, addrB)
	pkt := udpPkt(addrSelf, addrB, 1, 2, "now")
	e.mem.InjectFromOS(pkt)
	start := time.Now()
	got := e.readN(t, 16)
	if len(got) != 1 {
		t.Fatalf("got %d packets", len(got))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Read took %v", d)
	}
}

func TestInnerBatchLargerThanReadBufs(t *testing.T) {
	mem := NewMemTUN("batch", 1380).WithBatchSize(8)
	e := newEnvMem(t, mem, nil, addrB)
	var want [][]byte
	for i := 0; i < 8; i++ {
		p := udpPkt(addrSelf, addrB, 1, uint16(1000+i), "n")
		want = append(want, p)
		mem.InjectFromOS(p)
	}
	var got [][]byte
	for len(got) < 8 {
		got = append(got, e.readN(t, 3)...)
	}
	if len(got) != 8 {
		t.Fatalf("got %d packets", len(got))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("packet %d differs or is out of order", i)
		}
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxCopiesPerSecond = 100 }, addrB)
	pkt := udpPkt(addrSelf, addrBcast, 1, 2, "x")
	out := make(chan struct{}, 1024)
	go func() {
		bufs := make([][]byte, 32)
		for i := range bufs {
			bufs[i] = make([]byte, 2048)
		}
		sizes := make([]int, 32)
		for {
			n, err := e.d.Read(bufs, sizes, 0)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				out <- struct{}{}
			}
		}
	}()
	count := func() int {
		n := 0
		for {
			select {
			case <-out:
				n++
			case <-time.After(150 * time.Millisecond):
				return n
			}
		}
	}
	for i := 0; i < 40; i++ {
		e.mem.InjectFromOS(pkt)
	}
	// The bucket starts full (burst: rate/10, at least 16).
	if got := count(); got != 16 {
		t.Fatalf("burst delivered %d copies, want 16", got)
	}
	if s := e.d.Stats(); s.DroppedRate != 24 {
		t.Fatalf("DroppedRate = %d, want 24", s.DroppedRate)
	}
	// 100 ms at 100 copies per second refills 10 tokens.
	e.clock.Advance(100 * time.Millisecond)
	for i := 0; i < 40; i++ {
		e.mem.InjectFromOS(pkt)
	}
	if got := count(); got != 10 {
		t.Fatalf("after refill delivered %d copies, want 10", got)
	}
	// A long pause refills only up to the burst.
	e.clock.Advance(time.Hour)
	for i := 0; i < 40; i++ {
		e.mem.InjectFromOS(pkt)
	}
	if got := count(); got != 16 {
		t.Fatalf("after long pause delivered %d copies, want 16", got)
	}
}

func TestRateLimitDoesNotAffectUnicast(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxCopiesPerSecond = 1 }, addrB)
	for i := 0; i < 100; i++ {
		p := udpPkt(addrSelf, addrB, 1, uint16(2000+i), "u")
		e.mem.InjectFromOS(p)
		if got := e.readOne(t); !bytes.Equal(got, p) {
			t.Fatal("unicast changed")
		}
	}
}

func TestDefaultRateLimit(t *testing.T) {
	e := newEnv(t, nil, addrB)
	if e.d.limiter.rate != 2000 {
		t.Fatalf("default rate = %d, want 2000", e.d.limiter.rate)
	}
}

func TestInjectLocalBroadcast(t *testing.T) {
	e := newEnv(t, nil, addrB, addrC)
	loopback := netip.MustParseAddr("127.0.0.1")
	cap1 := udpPkt(loopback, addrGlobal, 4000, 4001, "captured announce")
	// The inner device stays idle; the packet must still come out promptly.
	e.d.InjectLocalBroadcast(cap1)
	got := e.readN(t, 16)
	if len(got) != 2 {
		t.Fatalf("got %d frames, want 2", len(got))
	}
	want := udpPkt(addrSelf, addrGlobal, 4000, 4001, "captured announce")
	checkFrame(t, got[0], addrSelf, addrB, want)
	checkFrame(t, got[1], addrSelf, addrC, want)
	src, _, _ := mustUDP(t, got[0][24:])
	if src.Addr() != addrSelf {
		t.Fatalf("inner source = %v, want %v", src.Addr(), addrSelf)
	}
	if s := e.d.Stats(); s.Injected != 1 {
		t.Fatalf("Injected = %d, want 1", s.Injected)
	}
}

func TestInjectLocalBroadcastZeroChecksumStaysZero(t *testing.T) {
	e := newEnv(t, nil, addrB)
	pkt := udpPkt(netip.MustParseAddr("192.168.1.20"), addrBcast, 4000, 4001, "no checksum")
	pkt[26], pkt[27] = 0, 0
	e.d.InjectLocalBroadcast(pkt)
	got := e.readOne(t)
	inner := got[24:]
	if inner[26] != 0 || inner[27] != 0 {
		t.Fatalf("UDP checksum became %x", inner[26:28])
	}
	if ipChecksum(inner[:20]) != 0 {
		t.Fatal("invalid IPv4 header checksum")
	}
	if src, _, _ := mustUDP(t, inner); src.Addr() != addrSelf {
		t.Fatalf("source = %v", src)
	}
}

// Windows hands the loopback copy of a locally sent datagram to the capture
// socket before checksum offload has filled in the UDP checksum: the field
// holds only the folded pseudo-header sum (measured on Windows 11 for a
// DirectPlay 4 announce and for a plain probe, D-034). The copy sent to the
// peers must carry a correct checksum, or the receiving stack drops it.
func TestInjectLocalBroadcastOffloadPlaceholderChecksum(t *testing.T) {
	e := newEnv(t, nil, addrB)
	local := netip.MustParseAddr("192.168.0.19")
	pkt := udpPkt(local, addrGlobal, 59161, 47624, "play\x02\x00\x0e\x00 enum sessions")
	udp := pkt[20:]
	src, dst := local.As4(), addrGlobal.As4()
	var pseudo uint64
	pseudo += uint64(binary.BigEndian.Uint16(src[0:])) + uint64(binary.BigEndian.Uint16(src[2:]))
	pseudo += uint64(binary.BigEndian.Uint16(dst[0:])) + uint64(binary.BigEndian.Uint16(dst[2:]))
	pseudo += protoUDP + uint64(len(udp))
	binary.BigEndian.PutUint16(udp[6:], csumFold(pseudo))
	if udpChecksumValid(src, dst, udp) {
		t.Fatal("test packet: placeholder checksum must be invalid")
	}
	e.d.InjectLocalBroadcast(pkt)
	got := e.readOne(t)
	s, d, payload := mustUDP(t, got[24:]) // fails on an invalid UDP checksum
	if s != netip.AddrPortFrom(addrSelf, 59161) || d != netip.AddrPortFrom(addrGlobal, 47624) ||
		string(payload) != "play\x02\x00\x0e\x00 enum sessions" {
		t.Fatalf("got %v -> %v %q", s, d, payload)
	}
}

func TestInjectLocalBroadcastDropRules(t *testing.T) {
	e := newEnv(t, nil, addrB)
	loopback := netip.MustParseAddr("127.0.0.1")
	e.d.InjectLocalBroadcast(ipv6Pkt())
	e.d.InjectLocalBroadcast(nil)
	e.d.InjectLocalBroadcast(udpPkt(loopback, addrB, 1, 2, "unicast"))
	e.d.InjectLocalBroadcast(rawTCP(loopback, addrGlobal, 1, 2))
	e.d.InjectLocalBroadcast(udpPkt(loopback, addrGlobal, 1, 138, "netbios"))
	good := udpPkt(loopback, addrGlobal, 1, 2, "sentinel")
	e.d.InjectLocalBroadcast(good)
	got := e.readN(t, 16)
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	checkFrame(t, got[0], addrSelf, addrB, udpPkt(addrSelf, addrGlobal, 1, 2, "sentinel"))
}

func TestInjectedPacketsAreCopied(t *testing.T) {
	e := newEnv(t, nil, addrB)
	pkt := udpPkt(netip.MustParseAddr("127.0.0.1"), addrGlobal, 1, 2, "original")
	orig := append([]byte(nil), pkt...)
	e.d.InjectLocalBroadcast(pkt)
	if !bytes.Equal(pkt, orig) {
		t.Fatal("InjectLocalBroadcast modified the caller's buffer")
	}
	for i := range pkt {
		pkt[i] = 0
	}
	got := e.readOne(t)
	checkFrame(t, got, addrSelf, addrB, udpPkt(addrSelf, addrGlobal, 1, 2, "original"))
}

func TestInjectQueueIsBoundedAndNeverBlocks(t *testing.T) {
	e := newEnv(t, nil, addrB)
	pkt := udpPkt(netip.MustParseAddr("127.0.0.1"), addrGlobal, 1, 2, "flood")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			e.d.InjectLocalBroadcast(pkt)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("InjectLocalBroadcast blocked")
	}
	if s := e.d.Stats(); s.DroppedQueue == 0 {
		t.Fatal("expected drops on a full queue")
	}
}

func TestInboundLanFrame(t *testing.T) {
	cases := []struct {
		name string
		dst  netip.Addr
	}{
		{"subnet broadcast", addrBcast},
		{"global broadcast", addrGlobal},
		{"multicast", addrMcast},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil, addrB)
			inner := udpPkt(addrB, tc.dst, 4000, 4001, "server announce")
			e.writeIn(t, lanFrame(addrB, addrSelf, inner))
			got := e.drainOS()
			if len(got) != 1 {
				t.Fatalf("delivered %d packets, want 1", len(got))
			}
			if !bytes.Equal(got[0], inner) {
				t.Fatalf("inner packet changed:\n got %x\nwant %x", got[0], inner)
			}
			if s := e.d.Stats(); s.LanFramesIn != 1 {
				t.Fatalf("LanFramesIn = %d", s.LanFramesIn)
			}
		})
	}
}

func TestInboundLanFrameRewritesSource(t *testing.T) {
	e := newEnv(t, nil, addrB)
	// The inner source claims to be somebody else; the tunnel knows the sender.
	inner := udpPkt(netip.MustParseAddr("192.168.1.77"), addrGlobal, 4000, 4001, "who am i")
	e.writeIn(t, lanFrame(addrB, addrSelf, inner))
	got := e.drainOS()
	if len(got) != 1 {
		t.Fatalf("delivered %d packets, want 1", len(got))
	}
	src, dst, payload := mustUDP(t, got[0]) // also verifies both checksums
	if src.Addr() != addrB || src.Port() != 4000 || dst.Addr() != addrGlobal || dst.Port() != 4001 || string(payload) != "who am i" {
		t.Fatalf("got %v -> %v %q", src, dst, payload)
	}
}

func TestInboundLanFrameZeroUDPChecksumStaysZero(t *testing.T) {
	e := newEnv(t, nil, addrB)
	inner := udpPkt(netip.MustParseAddr("192.168.1.77"), addrBcast, 4000, 4001, "nocsum")
	inner[26], inner[27] = 0, 0
	e.writeIn(t, lanFrame(addrB, addrSelf, inner))
	got := e.drainOS()
	if len(got) != 1 || got[0][26] != 0 || got[0][27] != 0 {
		t.Fatalf("zero UDP checksum not preserved: %x", got)
	}
	if ipChecksum(got[0][:20]) != 0 {
		t.Fatal("invalid IPv4 header checksum")
	}
	if src, _, _ := mustUDP(t, got[0]); src.Addr() != addrB {
		t.Fatalf("source = %v", src)
	}
}

func TestInboundMalformedLanFramesDropped(t *testing.T) {
	inner := udpPkt(addrB, addrGlobal, 4000, 4001, "payload")
	mod := func(f func(p []byte)) []byte {
		p := lanFrame(addrB, addrSelf, inner)
		f(p)
		return p
	}
	fixOuter := func(p []byte) {
		p[10], p[11] = 0, 0
		binary.BigEndian.PutUint16(p[10:], refChecksum(p[:20]))
	}
	innerOf := func(p []byte) []byte { return lanFrame(addrB, addrSelf, p) }
	badTTL := udpPkt(addrB, addrGlobal, 4000, 4001, "payload")
	badTTL[8] = 0
	binary.BigEndian.PutUint16(badTTL[10:], 0)
	binary.BigEndian.PutUint16(badTTL[10:], refChecksum(badTTL[:20]))
	innerFrag := udpPkt(addrB, addrGlobal, 4000, 4001, "payload")
	innerFrag[6] = 0x20
	innerTooLong := udpPkt(addrB, addrGlobal, 4000, 4001, "payload")
	binary.BigEndian.PutUint16(innerTooLong[2:], uint16(len(innerTooLong)+50))
	badUDPLen := udpPkt(addrB, addrGlobal, 4000, 4001, "payload")
	binary.BigEndian.PutUint16(badUDPLen[24:], 500)

	bad := map[string][]byte{
		"wrong magic":         mod(func(p []byte) { p[20] = 'X'; fixOuter(p) }),
		"wrong version":       mod(func(p []byte) { p[22] = 2 }),
		"nonzero flags":       mod(func(p []byte) { p[23] = 1 }),
		"no inner packet":     lanFrame(addrB, addrSelf, nil),
		"truncated inner":     lanFrame(addrB, addrSelf, inner[:15]),
		"outer bad checksum":  mod(func(p []byte) { p[10] ^= 0xFF }),
		"outer has options":   mod(func(p []byte) { p[0] = 0x46 }),
		"outer fragment":      mod(func(p []byte) { p[6] = 0x20; fixOuter(p) }),
		"outer not for us":    lanFrame(addrB, addrC, inner),
		"outer src outside":   lanFrame(netip.MustParseAddr("192.168.1.2"), addrSelf, inner),
		"outer src is self":   lanFrame(addrSelf, addrSelf, inner),
		"outer length lies":   mod(func(p []byte) { binary.BigEndian.PutUint16(p[2:], uint16(len(p)+8)); fixOuter(p) }),
		"inner ipv6":          innerOf(ipv6Pkt()),
		"inner tcp":           innerOf(rawTCP(addrB, addrGlobal, 1, 2)),
		"inner icmp":          innerOf(rawIPv4(addrB, addrGlobal, 1, 64, []byte{8, 0, 0, 0, 0, 0, 0, 0})),
		"inner unicast":       innerOf(udpPkt(addrB, addrC, 1, 2, "unicast")),
		"inner unicast self":  innerOf(udpPkt(addrB, addrSelf, 1, 2, "unicast to us")),
		"inner ttl 0":         innerOf(badTTL),
		"inner fragment":      innerOf(innerFrag),
		"inner length lies":   innerOf(innerTooLong),
		"inner udp length":    innerOf(badUDPLen),
		"inner blocked port":  innerOf(udpPkt(addrB, addrGlobal, 1, 137, "nb")),
		"inner blocked mcast": innerOf(udpPkt(addrB, addrMcast, 1, 138, "nb")),
		"nested lanframe":     innerOf(lanFrame(addrB, addrC, inner)),
	}
	e := newEnv(t, nil, addrB)
	good := lanFrame(addrB, addrSelf, inner)
	for name, p := range bad {
		e.writeIn(t, p, good)
		got := e.drainOS()
		if len(got) != 1 || !bytes.Equal(got[0], inner) {
			t.Errorf("%s: delivered %d packets, want only the good one", name, len(got))
		}
	}
	s := e.d.Stats()
	if s.LanFramesIn != uint64(len(bad)) {
		t.Errorf("LanFramesIn = %d, want %d (only the good frames)", s.LanFramesIn, len(bad))
	}
	if dropped := s.DroppedIPv6 + s.DroppedMalformed + s.DroppedBlocked + s.DroppedSpoofed + s.DroppedOther; dropped != uint64(len(bad)) {
		t.Errorf("total dropped = %d, want %d; %+v", dropped, len(bad), s)
	}
}

func TestInboundTTLIsRespected(t *testing.T) {
	e := newEnv(t, nil, addrB)
	inner := udpPkt(addrB, addrMcast, 1, 2, "ttl one")
	inner[8] = 1
	binary.BigEndian.PutUint16(inner[10:], 0)
	binary.BigEndian.PutUint16(inner[10:], refChecksum(inner[:20]))
	e.writeIn(t, lanFrame(addrB, addrSelf, inner))
	got := e.drainOS()
	if len(got) != 1 || got[0][8] != 1 {
		t.Fatalf("inner TTL not preserved: %x", got)
	}
}

func TestWriteWithOtherOffsets(t *testing.T) {
	e := newEnv(t, nil, addrB)
	inner := udpPkt(addrB, addrGlobal, 1, 2, "x")
	uni := udpPkt(addrB, addrSelf, 1, 2, "u")
	for _, off := range []int{0, 10, 16, 40} {
		bufs := make([][]byte, 2)
		for i, p := range [][]byte{lanFrame(addrB, addrSelf, inner), uni} {
			b := make([]byte, off+len(p))
			copy(b[off:], p)
			bufs[i] = b
		}
		if n, err := e.d.Write(bufs, off); n != 2 || err != nil {
			t.Fatalf("offset %d: Write = %d, %v", off, n, err)
		}
		got := e.drainOS()
		if len(got) != 2 || !bytes.Equal(got[0], inner) || !bytes.Equal(got[1], uni) {
			t.Fatalf("offset %d: wrong output", off)
		}
	}
}

func TestWriteAllDroppedMakesNoInnerCall(t *testing.T) {
	e := newEnv(t, nil, addrB)
	e.writeIn(t, ipv6Pkt(), rawTCP(addrB, addrSelf, 1, 445))
	if calls := e.mem.WriteCalls(); calls != 0 {
		t.Fatalf("inner Write calls = %d, want 0", calls)
	}
}

func TestWriteInnerErrorIsReturned(t *testing.T) {
	e := newEnv(t, nil, addrB)
	_ = e.mem.Close()
	b := make([]byte, testOffset+28)
	copy(b[testOffset:], udpPkt(addrB, addrSelf, 1, 2, ""))
	if _, err := e.d.Write([][]byte{b}, testOffset); err == nil {
		t.Fatal("expected the inner error")
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	e := newEnv(t, nil, addrB)
	errc := make(chan error, 1)
	go func() {
		bufs := [][]byte{make([]byte, 2048)}
		_, err := e.d.Read(bufs, make([]int, 1), 0)
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := e.d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Read error = %v, want os.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Read still blocked after Close")
	}
	// Close is idempotent and Read keeps failing.
	if err := e.d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := e.d.Read([][]byte{make([]byte, 2048)}, make([]int, 1), 0); err == nil {
		t.Fatal("Read after Close succeeded")
	}
	e.d.InjectLocalBroadcast(udpPkt(netip.MustParseAddr("127.0.0.1"), addrGlobal, 1, 2, "late")) // must not panic
}

func TestInnerReadErrorIsPropagatedAfterPackets(t *testing.T) {
	e := newEnv(t, nil, addrB)
	p := udpPkt(addrSelf, addrB, 1, 2, "last")
	e.mem.InjectFromOS(p)
	if got := e.readOne(t); !bytes.Equal(got, p) {
		t.Fatal("packet changed")
	}
	_ = e.mem.Close()
	_, err := e.tryRead(4, 3*time.Second)
	if err == nil {
		t.Fatal("expected an error after the inner device closed")
	}
}

func TestConcurrentUse(t *testing.T) {
	e := newEnv(t, nil, addrB, addrC)
	var stop atomic.Bool
	done := make(chan struct{})
	go func() { // reader
		defer close(done)
		bufs := make([][]byte, 16)
		for i := range bufs {
			bufs[i] = make([]byte, 2048)
		}
		sizes := make([]int, 16)
		for {
			if _, err := e.d.Read(bufs, sizes, 0); err != nil {
				return
			}
		}
	}()
	wdone := make(chan struct{})
	go func() { // writer
		defer close(wdone)
		in := []byte("in")
		for !stop.Load() {
			for _, p := range [][]byte{udpPkt(addrB, addrSelf, 1, 2, string(in)), lanFrame(addrB, addrSelf, udpPkt(addrB, addrGlobal, 1, 2, "x"))} {
				b := make([]byte, testOffset+len(p))
				copy(b[testOffset:], p)
				_, _ = e.d.Write([][]byte{b}, testOffset)
			}
			e.drainOS()
		}
	}()
	pkt := udpPkt(addrSelf, addrBcast, 1, 2, "out")
	cap1 := udpPkt(netip.MustParseAddr("127.0.0.1"), addrGlobal, 1, 2, "cap")
	for i := 0; i < 2000; i++ {
		e.mem.InjectFromOS(pkt)
		e.d.InjectLocalBroadcast(cap1)
		e.setPeers(addrB, addrC)
		_ = e.d.Stats()
	}
	stop.Store(true)
	<-wdone
	_ = e.d.Close()
	<-done
}
