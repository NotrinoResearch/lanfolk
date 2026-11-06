package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/stun"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

func startServer(t *testing.T, mod func(*Config)) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.UDPAddr = "127.0.0.1:0"
	cfg.TCPAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	if mod != nil {
		mod(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

type udpClient struct {
	t    *testing.T
	conn *net.UDPConn
	srv  netip.AddrPort
}

func newUDPClient(t *testing.T, s *Server) *udpClient {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &udpClient{t: t, conn: c, srv: s.UDPAddr()}
}

func (c *udpClient) send(b []byte) {
	if _, err := c.conn.WriteToUDPAddrPort(b, c.srv); err != nil {
		c.t.Fatal(err)
	}
}

func (c *udpClient) recv(timeout time.Duration) []byte {
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, _, err := c.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

func (c *udpClient) recvControl(typ byte) wire.RelayControl {
	c.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b := c.recv(time.Until(deadline))
		if b == nil {
			break
		}
		if m, err := wire.ParseRelayControl(b); err == nil && m.Type == typ {
			return m
		}
	}
	c.t.Fatalf("no relay control message of type %d", typ)
	return wire.RelayControl{}
}

func (c *udpClient) local() netip.AddrPort {
	return c.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

var sidA = wire.SessionID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func TestSTUN(t *testing.T) {
	s := startServer(t, nil)
	c := newUDPClient(t, s)
	tx := stun.NewTxID()
	c.send(stun.BindingRequest(tx))
	rtx, addr, err := stun.ParseBindingResponse(c.recv(2 * time.Second))
	if err != nil || rtx != tx {
		t.Fatalf("stun: %v", err)
	}
	if addr != c.local() {
		t.Fatalf("mapped %v want %v", addr, c.local())
	}
}

func TestPingPong(t *testing.T) {
	s := startServer(t, nil)
	c := newUDPClient(t, s)
	c.send(wire.AppendPing(nil, 77))
	m := c.recvControl(wire.RelayPong)
	if m.Nonce != 77 || m.Addr != c.local() {
		t.Fatalf("pong %+v", m)
	}
}

func TestForwardBetweenTwoParties(t *testing.T) {
	s := startServer(t, nil)
	a, b := newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	if m := a.recvControl(wire.RelayRegisterAck); m.PeerPresent {
		t.Fatal("peer present before second party")
	}
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	if m := b.recvControl(wire.RelayRegisterAck); !m.PeerPresent {
		t.Fatal("second party does not see the first")
	}
	if m := a.recvControl(wire.RelayPeerJoined); m.Session != sidA {
		t.Fatal("peer joined for wrong session")
	}
	a.send(wire.AppendRelayData(nil, sidA, []byte("ping")))
	_, p, ok := wire.ParseRelayData(b.recv(2 * time.Second))
	if !ok || string(p) != "ping" {
		t.Fatalf("b got %q", p)
	}
	b.send(wire.AppendRelayData(nil, sidA, []byte("pong")))
	_, p, ok = wire.ParseRelayData(a.recv(2 * time.Second))
	if !ok || string(p) != "pong" {
		t.Fatalf("a got %q", p)
	}
}

func TestUnregisteredSourceDropped(t *testing.T) {
	s := startServer(t, nil)
	a, b, evil := newUDPClient(t, s), newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	a.recvControl(wire.RelayPeerJoined)
	evil.send(wire.AppendRelayData(nil, sidA, []byte("inject")))
	if got := b.recv(300 * time.Millisecond); got != nil {
		t.Fatalf("injected data forwarded: %q", got)
	}
}

func TestThirdPartyRejected(t *testing.T) {
	s := startServer(t, nil)
	a, b, c := newUDPClient(t, s), newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	c.send(wire.AppendRegister(nil, sidA, wire.Token{3}))
	if m := c.recvControl(wire.RelayReject); m.Code != wire.RejectSessionFull {
		t.Fatalf("code %d", m.Code)
	}
}

func TestPartyRoamsWithSameToken(t *testing.T) {
	s := startServer(t, nil)
	a, b, b2 := newUDPClient(t, s), newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	a.recvControl(wire.RelayPeerJoined)
	// b's NAT mapping changes: same token from a new address.
	b2.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b2.recvControl(wire.RelayRegisterAck)
	a.send(wire.AppendRelayData(nil, sidA, []byte("x")))
	if _, p, ok := wire.ParseRelayData(b2.recv(2 * time.Second)); !ok || string(p) != "x" {
		t.Fatal("data not delivered to new address")
	}
}

// expectData waits for relayed data with payload want on c.
func expectData(t *testing.T, c *udpClient, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b := c.recv(time.Until(deadline))
		if b == nil {
			break
		}
		if _, p, ok := wire.ParseRelayData(b); ok && string(p) == want {
			return
		}
	}
	t.Fatalf("data %q not delivered", want)
}

// noData reports a failure if relayed data arrives on c within d.
func noData(t *testing.T, c *udpClient, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b := c.recv(time.Until(deadline))
		if b == nil {
			return
		}
		if _, p, ok := wire.ParseRelayData(b); ok {
			t.Fatalf("unexpected data %q", p)
		}
	}
}

// 6 November: a client restarted on the same UDP port without its Unregister
// reaching the relay while its friend was offline. The new run registered
// next to the old one, its own traffic (matched by address) kept the old
// party fresh, and the friend was told SessionFull for as long as the client
// ran; the client's frames came back to itself (D-041).
func TestRestartOnSameAddressTakesOverItsSlot(t *testing.T) {
	s := startServer(t, func(c *Config) { c.IdleTimeout = 2 * time.Second })
	z, o := newUDPClient(t, s), newUDPClient(t, s)
	z.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	z.recvControl(wire.RelayRegisterAck)
	// Killed; the next run uses the same socket address and a new token.
	z.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	if m := z.recvControl(wire.RelayRegisterAck); m.PeerPresent {
		t.Fatal("the new run sees its own earlier run as the peer")
	}
	// The new run keeps talking (Hellos), more often than staleParty.
	z.send(wire.AppendRelayData(nil, sidA, []byte("hello")))
	noData(t, z, 300*time.Millisecond)
	z.send(wire.AppendRelayData(nil, sidA, []byte("hello")))
	o.send(wire.AppendRegister(nil, sidA, wire.Token{3}))
	if m := o.recvControl(wire.RelayRegisterAck); !m.PeerPresent {
		t.Fatal("friend registered but the client is not present")
	}
	z.recvControl(wire.RelayPeerJoined)
	z.send(wire.AppendRelayData(nil, sidA, []byte("z")))
	expectData(t, o, "z")
	o.send(wire.AppendRelayData(nil, sidA, []byte("o")))
	expectData(t, z, "o")
	// The dead run's Unregister, if it arrives late, does not remove the
	// new run.
	z.send(wire.AppendUnregister(nil, sidA, wire.Token{1}))
	o.send(wire.AppendRelayData(nil, sidA, []byte("o2")))
	expectData(t, z, "o2")
	if got := s.metrics.takeovers.Load(); got != 1 {
		t.Fatalf("takeovers = %d", got)
	}
}

func TestRestartOnSameAddressWhilePeerPresent(t *testing.T) {
	s := startServer(t, nil)
	z, o := newUDPClient(t, s), newUDPClient(t, s)
	z.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	z.recvControl(wire.RelayRegisterAck)
	o.send(wire.AppendRegister(nil, sidA, wire.Token{3}))
	o.recvControl(wire.RelayRegisterAck)
	z.recvControl(wire.RelayPeerJoined)
	z.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	if m := z.recvControl(wire.RelayRegisterAck); !m.PeerPresent {
		t.Fatal("peer not present after the restart")
	}
	// The friend hears about the new run at once (it sends a Hello).
	o.recvControl(wire.RelayPeerJoined)
	// The old token is gone: a refresh with it is a new party, and the
	// session is full of fresh parties.
	z2 := newUDPClient(t, s)
	z2.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	if m := z2.recvControl(wire.RelayReject); m.Code != wire.RejectSessionFull {
		t.Fatalf("code %d", m.Code)
	}
}

func TestRoamingOntoTheOtherSlotsAddressReplacesIt(t *testing.T) {
	s := startServer(t, nil)
	a, b, c := newUDPClient(t, s), newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	// Token 2 now comes from a's address: a's run is gone, its address
	// belongs to token 2's client.
	a.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	if m := a.recvControl(wire.RelayRegisterAck); m.PeerPresent {
		t.Fatal("a party at the same address is still counted as the peer")
	}
	c.send(wire.AppendRegister(nil, sidA, wire.Token{3}))
	if m := c.recvControl(wire.RelayRegisterAck); !m.PeerPresent {
		t.Fatal("new party not admitted")
	}
	c.send(wire.AppendRelayData(nil, sidA, []byte("c")))
	expectData(t, a, "c")
}

func TestPerIPSessionLimit(t *testing.T) {
	s := startServer(t, func(c *Config) { c.MaxSessionsPerIP = 3 })
	a := newUDPClient(t, s)
	for i := 0; i < 3; i++ {
		sid := sidA
		sid[0] = byte(i + 10)
		a.send(wire.AppendRegister(nil, sid, wire.Token{1}))
		a.recvControl(wire.RelayRegisterAck)
	}
	sid := sidA
	sid[0] = 99
	a.send(wire.AppendRegister(nil, sid, wire.Token{1}))
	if m := a.recvControl(wire.RelayReject); m.Code != wire.RejectSessionLimit {
		t.Fatalf("code %d", m.Code)
	}
}

func TestRateLimit(t *testing.T) {
	s := startServer(t, func(c *Config) { c.MaxSessionKbps = 80 }) // 10 kB/s, burst 10 kB
	a, b := newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	b.recvControl(wire.RelayRegisterAck)
	payload := make([]byte, 1000)
	for i := 0; i < 50; i++ {
		a.send(wire.AppendRelayData(nil, sidA, payload))
	}
	got := 0
	for b.recv(200*time.Millisecond) != nil {
		got++
	}
	if got >= 50 || got < 5 {
		t.Fatalf("received %d of 50 packets; rate limit not applied", got)
	}
	if s.metrics.droppedRate.Load() == 0 {
		t.Fatal("rate drops not counted")
	}
}

func TestIdleTimeout(t *testing.T) {
	s := startServer(t, func(c *Config) {
		c.IdleTimeout = 200 * time.Millisecond
		c.SweepInterval = 50 * time.Millisecond
	})
	a, b := newUDPClient(t, s), newUDPClient(t, s)
	a.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	a.recvControl(wire.RelayRegisterAck)
	time.Sleep(500 * time.Millisecond)
	b.send(wire.AppendRegister(nil, sidA, wire.Token{2}))
	if m := b.recvControl(wire.RelayRegisterAck); m.PeerPresent {
		t.Fatal("idle party was not expired")
	}
}

func TestTCPToUDPForwarding(t *testing.T) {
	s := startServer(t, nil)
	u := newUDPClient(t, s)
	u.send(wire.AppendRegister(nil, sidA, wire.Token{1}))
	u.recvControl(wire.RelayRegisterAck)

	tc, err := net.Dial("tcp", s.TCPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	tc.Write(wire.AppendStreamFrame(nil, wire.AppendRegister(nil, sidA, wire.Token{2})))
	buf := make([]byte, 4096)
	tc.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := wire.ReadStreamFrame(tc, buf)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := wire.ParseRelayControl(f); err != nil || m.Type != wire.RelayRegisterAck || !m.PeerPresent {
		t.Fatalf("tcp ack %+v %v", m, err)
	}
	u.recvControl(wire.RelayPeerJoined)
	u.send(wire.AppendRelayData(nil, sidA, []byte("to-tcp")))
	f, err = wire.ReadStreamFrame(tc, buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, p, ok := wire.ParseRelayData(f); !ok || string(p) != "to-tcp" {
		t.Fatalf("tcp got %q", p)
	}
	tc.Write(wire.AppendStreamFrame(nil, wire.AppendRelayData(nil, sidA, []byte("to-udp"))))
	if _, p, ok := wire.ParseRelayData(u.recv(2 * time.Second)); !ok || string(p) != "to-udp" {
		t.Fatalf("udp got %q", p)
	}
	// Closing the TCP connection removes its party.
	tc.Close()
	time.Sleep(100 * time.Millisecond)
	u2 := newUDPClient(t, s)
	u2.send(wire.AppendRegister(nil, sidA, wire.Token{3}))
	if m := u2.recvControl(wire.RelayRegisterAck); !m.PeerPresent {
		t.Fatal("expected the UDP party to remain")
	}
}

func TestDelayedEcho(t *testing.T) {
	s := startServer(t, nil)
	c := newUDPClient(t, s)
	start := time.Now()
	c.send(wire.AppendEchoRequest(nil, 5, 1))
	m := c.recvControl(wire.RelayEchoReply)
	if m.Nonce != 5 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("echo %+v after %v", m, time.Since(start))
	}
}

func TestMetricsAndHealth(t *testing.T) {
	s := startServer(t, nil)
	c := newUDPClient(t, s)
	c.send(stun.BindingRequest(stun.NewTxID()))
	c.recv(time.Second)
	base := "http://" + s.MetricsAddr().String()
	resp, err := http.Get(base + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	resp, err = http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "lanfolk_relay_stun_requests_total 1") {
		t.Fatalf("metrics: %s", body)
	}
}
