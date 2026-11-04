package relay

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

func TestLimitKeyGroupsIPv6By64(t *testing.T) {
	a := netip.MustParseAddr("2001:db8:1:2:aaaa::1")
	b := netip.MustParseAddr("2001:db8:1:2:bbbb::2")
	c := netip.MustParseAddr("2001:db8:1:3::1")
	if limitKey(a) != limitKey(b) {
		t.Fatal("two addresses of one /64 must share a limit key")
	}
	if limitKey(a) == limitKey(c) {
		t.Fatal("different /64s must not share a limit key")
	}
	v4 := netip.MustParseAddr("198.51.100.7")
	if limitKey(v4) != v4 {
		t.Fatal("IPv4 addresses are their own key")
	}
}

func TestIPBucketsNeverLockOutNewSources(t *testing.T) {
	l := newIPBuckets(1, 1)
	now := time.Now().UnixNano()
	// The old limiter refused every new source once it tracked 200000
	// addresses. A flood from 300000 distinct addresses must not do that.
	for i := 0; i < 300000; i++ {
		ip := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		l.allow(ip, 1, now)
	}
	fresh := netip.MustParseAddr("203.0.113.99")
	// The fresh address may share a slot with a flooded one; after the
	// refill time of one token it must be allowed in any case.
	if !l.allow(fresh, 1, now+int64(time.Second)) {
		t.Fatal("a new source is locked out after a flood from many addresses")
	}
	if l.allow(fresh, 1, now+int64(time.Second)) {
		t.Fatal("burst of one token allowed two requests")
	}
}

func TestIPBucketsRateAndNoAllocs(t *testing.T) {
	l := newIPBuckets(100, 10) // 100 units/s, burst 10
	ip := netip.MustParseAddr("192.0.2.1")
	now := time.Now().UnixNano()
	n := 0
	for i := 0; i < 50; i++ {
		if l.allow(ip, 1, now) {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("allowed %d at one instant, want the burst of 10", n)
	}
	if !l.allow(ip, 1, now+int64(20*time.Millisecond)) { // 2 units refilled
		t.Fatal("bucket did not refill")
	}
	ip6 := netip.MustParseAddr("2001:db8::1")
	allocs := testing.AllocsPerRun(1000, func() {
		now += int64(time.Millisecond)
		l.allow(ip, 1, now)
		l.allow(ip6, 1, now)
	})
	if allocs != 0 {
		t.Fatalf("limiter allocates %v times per call; the packet path must not allocate", allocs)
	}
}

// pair registers two UDP parties of sid and returns them.
func pair(t *testing.T, s *Server, sid wire.SessionID) (*udpClient, *udpClient) {
	t.Helper()
	a, b := newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sid, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sid, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	a.recvControl(wire.RelayPeerJoined)
	return a, b
}

func drain(c *udpClient) int {
	n := 0
	for c.recv(200*time.Millisecond) != nil {
		n++
	}
	return n
}

func TestPerIPDataCap(t *testing.T) {
	// Sessions may each send 100 Mbit/s, but one source IP only 80 kbit/s
	// (10 kB/s with a burst of 10 kB) over all of its sessions.
	s := startServer(t, func(c *Config) {
		c.MaxSessionKbps = 100000
		c.MaxIPKbps = 80
		c.MaxTotalMbps = 0
	})
	sid2 := sidA
	sid2[0] = 77
	a1, b1 := pair(t, s, sidA)
	a2, b2 := pair(t, s, sid2)
	payload := make([]byte, 1000)
	for i := 0; i < 30; i++ {
		a1.send(wire.AppendRelayData(nil, sidA, payload))
		a2.send(wire.AppendRelayData(nil, sid2, payload))
	}
	got := drain(b1) + drain(b2)
	if got >= 60 || got < 5 {
		t.Fatalf("delivered %d of 60 packets; the per-IP cap was not applied", got)
	}
	if s.metrics.droppedIPRate.Load() == 0 {
		t.Fatal("per-IP drops not counted")
	}
	if s.metrics.droppedRate.Load() != 0 {
		t.Fatal("the session cap dropped packets although it is far above the traffic")
	}
}

func TestTotalDataCap(t *testing.T) {
	s := startServer(t, func(c *Config) {
		c.MaxSessionKbps = 100000
		c.MaxIPKbps = 0
		c.MaxTotalMbps = 1 // 125 kB/s, burst 125 kB
	})
	a, b := pair(t, s, sidA)
	payload := make([]byte, 1400)
	done := make(chan int)
	go func() { done <- drain(b) }()
	for i := 0; i < 200; i++ {
		a.send(wire.AppendRelayData(nil, sidA, payload))
	}
	<-done
	if s.metrics.droppedTotalRate.Load() == 0 {
		t.Fatal("280 kB in one burst passed a 125 kB total cap")
	}
	if s.metrics.droppedIPRate.Load() != 0 {
		t.Fatal("per-IP cap is off but dropped packets")
	}
}

func dialTCP(t *testing.T, s *Server) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", s.TCPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// closedByServer reports whether the server closed c within d.
func closedByServer(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	var b [64]byte
	for {
		_, err := c.Read(b[:])
		if err == nil {
			continue
		}
		var ne net.Error
		return !(errors.As(err, &ne) && ne.Timeout()) && !errors.Is(err, os.ErrDeadlineExceeded)
	}
}

func tcpControl(t *testing.T, c net.Conn, frame []byte, typ byte) {
	t.Helper()
	if _, err := c.Write(wire.AppendStreamFrame(nil, frame)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, wire.MaxStreamFrame)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		f, err := wire.ReadStreamFrame(c, buf)
		if err != nil {
			t.Fatalf("waiting for control type %d: %v", typ, err)
		}
		if m, err := wire.ParseRelayControl(f); err == nil && m.Type == typ {
			return
		}
	}
}

func TestTCPGlobalCap(t *testing.T) {
	s := startServer(t, func(c *Config) { c.MaxTCPConns = 2 })
	c1, c2 := dialTCP(t, s), dialTCP(t, s)
	tcpControl(t, c1, wire.AppendPing(nil, 1), wire.RelayPong)
	tcpControl(t, c2, wire.AppendPing(nil, 2), wire.RelayPong)
	c3 := dialTCP(t, s)
	if !closedByServer(c3, 2*time.Second) {
		t.Fatal("a connection above the total cap was kept")
	}
	if s.metrics.tcpRejects.Load() == 0 {
		t.Fatal("TCP rejects not counted")
	}
	// Closing one frees a slot.
	c1.Close()
	time.Sleep(100 * time.Millisecond)
	c4 := dialTCP(t, s)
	tcpControl(t, c4, wire.AppendPing(nil, 4), wire.RelayPong)
}

func TestTCPIdleWithoutSessionIsClosed(t *testing.T) {
	s := startServer(t, func(c *Config) {
		c.TCPGrace = 200 * time.Millisecond
		c.SweepInterval = 50 * time.Millisecond
	})
	silent := dialTCP(t, s)
	withSession := dialTCP(t, s)
	tcpControl(t, withSession, wire.AppendRegister(nil, sidA, wire.Token{1}), wire.RelayRegisterAck)
	pinger := dialTCP(t, s)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(80 * time.Millisecond):
				pinger.Write(wire.AppendStreamFrame(nil, wire.AppendPing(nil, 9)))
			}
		}
	}()
	if !closedByServer(silent, 2*time.Second) {
		t.Fatal("a silent connection without a session was kept")
	}
	if s.metrics.tcpIdleClosed.Load() == 0 {
		t.Fatal("idle close not counted")
	}
	time.Sleep(400 * time.Millisecond)
	tcpControl(t, withSession, wire.AppendPing(nil, 3), wire.RelayPong)
	// The pinging client without sessions is still connected.
	if s.Status().TCPConns != 2 {
		t.Fatalf("%d TCP connections, want the two active ones", s.Status().TCPConns)
	}
	// After its session is removed, the grace time applies to it too.
	withSession.Write(wire.AppendStreamFrame(nil, wire.AppendUnregister(nil, sidA, wire.Token{1})))
	if !closedByServer(withSession, 2*time.Second) {
		t.Fatal("connection stayed open after its last session was removed")
	}
}

func TestEchoPerIPCap(t *testing.T) {
	s := startServer(t, func(c *Config) { c.MaxEchoesPerIP = 2 })
	c := newUDPClient(t, s)
	c.send(wire.AppendEchoRequest(nil, 1, 1))
	c.send(wire.AppendEchoRequest(nil, 2, 1))
	c.send(wire.AppendEchoRequest(nil, 3, 1))
	if m := c.recvControl(wire.RelayReject); m.Code != wire.RejectBusy {
		t.Fatalf("code %d", m.Code)
	}
	if s.metrics.echoRejects.Load() != 1 {
		t.Fatalf("echo rejects %d", s.metrics.echoRejects.Load())
	}
	// The two accepted echoes still arrive, and free the slots.
	c.recvControl(wire.RelayEchoReply)
	time.Sleep(200 * time.Millisecond)
	s.echoMu.Lock()
	left := len(s.echoIP)
	s.echoMu.Unlock()
	if left != 0 || s.echoes.Load() != 0 {
		t.Fatalf("echo accounting leaked: %d keys, %d pending", left, s.echoes.Load())
	}
}

func TestNoChallengeInNormalOperation(t *testing.T) {
	s := startServer(t, nil)
	pair(t, s, sidA)
	if s.metrics.challenges.Load() != 0 || s.pressure.Load() {
		t.Fatal("an idle relay asked for a cookie")
	}
}

func TestChallengeAlways(t *testing.T) {
	s := startServer(t, func(c *Config) { c.Challenge = ChallengeAlways })
	a := newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	ch := a.recvControl(wire.RelayChallenge)
	if ch.Session != sidA {
		t.Fatal("challenge for the wrong session")
	}
	if st := s.Status(); st.Sessions != 0 {
		t.Fatal("a challenged Register created state")
	}

	// A wrong cookie is challenged again and counted.
	bad := ch.Cookie
	bad[0] ^= 1
	a.send(wire.AppendRegisterCookie(nil, sidA, wire.Token{1}, bad))
	a.recvControl(wire.RelayChallenge)
	if s.metrics.cookieRejects.Load() != 1 {
		t.Fatalf("cookie rejects %d", s.metrics.cookieRejects.Load())
	}

	// Another address cannot use a's cookie.
	other := newUDPClient(t, s)
	other.send(wire.AppendRegisterCookie(nil, sidA, wire.Token{1}, ch.Cookie))
	other.recvControl(wire.RelayChallenge)

	// The right cookie registers.
	a.send(wire.AppendRegisterCookie(nil, sidA, wire.Token{1}, ch.Cookie))
	if m := a.recvControl(wire.RelayRegisterAck); m.PeerPresent {
		t.Fatal("peer present")
	}
	// A refresh of a known party needs no cookie.
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)

	// TCP has proven its address with the handshake: no challenge.
	tc := dialTCP(t, s)
	tcpControl(t, tc, wire.AppendRegister(nil, sidA, wire.Token{2}), wire.RelayRegisterAck)
}

func TestCookieWindows(t *testing.T) {
	c := newCookieMaker()
	src := netip.MustParseAddrPort("198.51.100.7:4000")
	now := int64(1_800_000_000)
	ck := c.current(src, sidA, now)
	if !c.valid(src, sidA, ck, now) || !c.valid(src, sidA, ck, now+cookieWindowSec) {
		t.Fatal("cookie must be valid in its window and the next")
	}
	if c.valid(src, sidA, ck, now+2*cookieWindowSec) {
		t.Fatal("expired cookie accepted")
	}
	other := sidA
	other[15] ^= 1
	if c.valid(src, other, ck, now) {
		t.Fatal("cookie accepted for another session")
	}
	if c.valid(netip.MustParseAddrPort("198.51.100.7:4001"), sidA, ck, now) {
		t.Fatal("cookie accepted for another port")
	}
	if newCookieMaker().valid(src, sidA, ck, now) {
		t.Fatal("cookie accepted by a relay with another secret")
	}
}

func sidN(i int) wire.SessionID {
	s := sidA
	s[0], s[1] = byte(i), byte(i>>8)
	return s
}

func TestChallengeWhenSessionTableFills(t *testing.T) {
	s := startServer(t, func(c *Config) {
		c.MaxSessions = 10
		c.ChallengeSessionsPct = 50
		c.ChallengeNewPerSec = 0
	})
	a := newUDPClient(t, s)
	for i := 0; i < 5; i++ {
		a.send(wire.AppendRegister(nil, sidN(i), wire.Token{1}))
		a.recvControl(wire.RelayRegisterAck)
	}
	a.send(wire.AppendRegister(nil, sidN(5), wire.Token{1}))
	ch := a.recvControl(wire.RelayChallenge)
	if !s.pressure.Load() {
		t.Fatal("pressure flag not set")
	}
	// A client that answers gets in, an old client does not.
	a.send(wire.AppendRegisterCookie(nil, sidN(5), wire.Token{1}, ch.Cookie))
	a.recvControl(wire.RelayRegisterAck)
}

func TestChallengeWhenNewSessionsArriveTooFast(t *testing.T) {
	s := startServer(t, func(c *Config) {
		c.ChallengeSessionsPct = 0
		c.ChallengeNewPerSec = 3
	})
	a := newUDPClient(t, s)
	for i := 0; i < 3; i++ {
		a.send(wire.AppendRegister(nil, sidN(i), wire.Token{1}))
		a.recvControl(wire.RelayRegisterAck)
	}
	a.send(wire.AppendRegister(nil, sidN(3), wire.Token{1}))
	a.recvControl(wire.RelayChallenge)

	// Pressure ends after the hold time: simulate it.
	s.mu.Lock()
	s.pressureUntil = time.Now().Add(-time.Second).UnixNano()
	s.newWindow = 0
	s.mu.Unlock()
	a.send(wire.AppendRegister(nil, sidN(4), wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	if s.pressure.Load() {
		t.Fatal("pressure flag still set after the hold time")
	}
}
