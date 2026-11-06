package transport

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/relay"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.zx2c4.com/wireguard/conn"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []conn.Endpoint
}

func (f *fakeSender) SendWireGuard(pkt []byte, ep conn.Endpoint) error {
	f.mu.Lock()
	f.sent = append(f.sent, ep)
	f.mu.Unlock()
	return nil
}

func TestBindDeliverReceiveClose(t *testing.T) {
	s := &fakeSender{}
	b := NewBind(s, 4321)
	fns, port, err := b.Open(0)
	if err != nil || port != 4321 || len(fns) != 1 {
		t.Fatalf("open: %v %d", err, port)
	}
	if _, _, err := b.Open(0); err == nil {
		t.Fatal("double open accepted")
	}
	k, _ := identity.Generate()
	ep := NewPeerEndpoint(k.Public())
	b.Deliver([]byte{4, 0, 0, 0, 1, 2}, ep)
	b.Deliver([]byte{4, 0, 0, 0, 3}, ep)
	bufs := [][]byte{make([]byte, 2000), make([]byte, 2000), make([]byte, 2000)}
	sizes := make([]int, 3)
	eps := make([]conn.Endpoint, 3)
	n, err := fns[0](bufs, sizes, eps)
	if err != nil || n != 2 || sizes[0] != 6 || sizes[1] != 5 || eps[0] != ep {
		t.Fatalf("receive: n=%d err=%v sizes=%v", n, err, sizes)
	}
	done := make(chan error)
	go func() {
		_, err := fns[0](bufs, sizes, eps)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	b.Close()
	select {
	case err := <-done:
		if err != net.ErrClosed {
			t.Fatalf("receive after close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("receive did not unblock on close")
	}
	// Re-open works and packets delivered while closed were dropped.
	b.Deliver([]byte{4, 0, 0, 0}, ep)
	fns, _, err = b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send([][]byte{{1}}, ep); err != nil || len(s.sent) != 1 {
		t.Fatal("send not forwarded to sender")
	}
	b.Close()
}

func TestParseEndpoint(t *testing.T) {
	b := NewBind(&fakeSender{}, 1)
	k, _ := identity.Generate()
	ep, err := b.ParseEndpoint(NewPeerEndpoint(k.Public()).DstToString())
	if err != nil {
		t.Fatal(err)
	}
	pe, ok := ep.(*PeerEndpoint)
	if !ok || pe.Key != k.Public() {
		t.Fatal("peer endpoint round trip")
	}
	if !pe.DstIP().IsValid() || pe.DstIP() == NewPeerEndpoint(identity.PublicKey{1}).DstIP() {
		t.Fatal("peer endpoints need distinct synthetic addresses")
	}
	ep, err = b.ParseEndpoint("192.0.2.1:51820")
	if err != nil || ep.(*RawEndpoint).Addr != netip.MustParseAddrPort("192.0.2.1:51820") {
		t.Fatal("raw endpoint parse")
	}
}

func TestSocketFilterAndRoundTrip(t *testing.T) {
	a, err := ListenSocket("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	bs, _ := ListenSocket("127.0.0.1:0")
	defer bs.Close()
	got := make(chan string, 4)
	go bs.Run(func(p []byte, from netip.AddrPort) { got <- string(p) })
	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), bs.Port())
	a.WriteTo([]byte("one"), dst)
	if v := <-got; v != "one" {
		t.Fatal(v)
	}
	a.SetFilter(func(addr netip.AddrPort) bool { return addr == dst })
	a.WriteTo([]byte("blocked"), dst)
	select {
	case v := <-got:
		t.Fatalf("filtered packet delivered: %s", v)
	case <-time.After(150 * time.Millisecond):
	}
}

func startRelay(t *testing.T) *relay.Server {
	cfg := relay.DefaultConfig()
	cfg.UDPAddr, cfg.TCPAddr, cfg.MetricsAddr = "127.0.0.1:0", "127.0.0.1:0", ""
	s, err := relay.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

type relayHarness struct {
	sock   *Socket
	client *RelayClient
	data   chan string
	joined chan wire.SessionID
}

func newHarness(t *testing.T, srv *relay.Server, opts RelayOptions) *relayHarness {
	sock, err := ListenSocket("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &relayHarness{sock: sock, data: make(chan string, 16), joined: make(chan wire.SessionID, 4)}
	tcpPort := srv.TCPAddr().(*net.TCPAddr).Port
	opts.Host = "127.0.0.1"
	opts.UDPPort = int(srv.UDPAddr().Port())
	opts.TCPPort = tcpPort
	opts.OnData = func(sid wire.SessionID, payload []byte, viaTCP bool) { h.data <- string(payload) }
	opts.OnPeerJoined = func(sid wire.SessionID) { h.joined <- sid }
	h.client = NewRelayClient(sock, opts)
	go sock.Run(func(p []byte, from netip.AddrPort) {
		if h.client.IsRelayAddr(from) {
			h.client.HandleFrame(p, false)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go h.client.Run(ctx)
	t.Cleanup(func() { cancel(); sock.Close() })
	return h
}

func waitMode(t *testing.T, c *RelayClient, m Mode) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if c.Mode() == m {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("relay mode %v, want %v", c.Mode(), m)
}

func fastOpts() RelayOptions {
	return RelayOptions{PingInterval: 100 * time.Millisecond, DownAfter: 400 * time.Millisecond,
		UDPRetryInterval: 300 * time.Millisecond, RegisterInterval: 500 * time.Millisecond}
}

func TestRelayClientUDP(t *testing.T) {
	srv := startRelay(t)
	a := newHarness(t, srv, fastOpts())
	b := newHarness(t, srv, fastOpts())
	waitMode(t, a.client, ModeUDP)
	waitMode(t, b.client, ModeUDP)
	if !a.client.Observed().IsValid() || a.client.RTT() <= 0 {
		t.Fatal("observed address or RTT missing")
	}
	sid := wire.SessionID{7}
	a.client.Register(sid)
	b.client.Register(sid)
	// Registrations race; whichever side registered first gets PeerJoined,
	// the other learns presence from its RegisterAck.
	deadline := time.Now().Add(3 * time.Second)
	for !(a.client.PeerPresent(sid) && b.client.PeerPresent(sid)) {
		if time.Now().After(deadline) {
			t.Fatal("peers do not see each other at the relay")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-a.joined:
	case <-b.joined:
	case <-time.After(time.Second):
		t.Fatal("peer joined not reported to the first party")
	}
	if err := a.client.SendData(sid, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := <-b.data; got != "hello" {
		t.Fatal(got)
	}
}

func TestRelayClientTCPFallbackAndReturn(t *testing.T) {
	srv := startRelay(t)
	relayUDP := srv.UDPAddr()
	a := newHarness(t, srv, fastOpts())
	b := newHarness(t, srv, fastOpts())
	// a's network blocks UDP to the relay.
	a.sock.SetFilter(func(addr netip.AddrPort) bool { return addr.Port() == relayUDP.Port() })
	waitMode(t, a.client, ModeTCP)
	waitMode(t, b.client, ModeUDP)
	sid := wire.SessionID{9}
	a.client.Register(sid)
	b.client.Register(sid)
	time.Sleep(300 * time.Millisecond)
	b.client.SendData(sid, []byte("over-tcp"))
	select {
	case got := <-a.data:
		if got != "over-tcp" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no data over TCP fallback")
	}
	// UDP comes back: the client returns to UDP and keeps the session.
	a.sock.SetFilter(nil)
	waitMode(t, a.client, ModeUDP)
	time.Sleep(300 * time.Millisecond)
	b.client.SendData(sid, []byte("over-udp"))
	select {
	case got := <-a.data:
		if got != "over-udp" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no data after returning to UDP")
	}
}

func TestRelayClientAnswersChallenge(t *testing.T) {
	cfg := relay.DefaultConfig()
	cfg.UDPAddr, cfg.TCPAddr, cfg.MetricsAddr = "127.0.0.1:0", "127.0.0.1:0", ""
	cfg.Challenge = relay.ChallengeAlways // as if the relay were under attack
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	a := newHarness(t, srv, fastOpts())
	b := newHarness(t, srv, fastOpts())
	waitMode(t, a.client, ModeUDP)
	waitMode(t, b.client, ModeUDP)
	sid := wire.SessionID{11}
	a.client.Register(sid)
	b.client.Register(sid)
	deadline := time.Now().Add(3 * time.Second)
	for !(a.client.PeerPresent(sid) && b.client.PeerPresent(sid)) {
		if time.Now().After(deadline) {
			t.Fatal("clients did not register through the cookie challenge")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := a.client.SendData(sid, []byte("through")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-b.data:
		if got != "through" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no data after the challenge")
	}
}

// A Register the relay rejects as full clears presence, is reported once per
// change and is retried after fullRetry instead of the refresh interval
// (D-041).
func TestRelayClientSessionFull(t *testing.T) {
	srv := startRelay(t)
	opts := fastOpts()
	opts.RegisterInterval = 30 * time.Second
	a := newHarness(t, srv, opts)
	b := newHarness(t, srv, opts)
	var mu sync.Mutex
	var rejects []byte
	opts.OnReject = func(_ wire.SessionID, code byte) {
		mu.Lock()
		rejects = append(rejects, code)
		mu.Unlock()
	}
	c := newHarness(t, srv, opts)
	for _, h := range []*relayHarness{a, b, c} {
		waitMode(t, h.client, ModeUDP)
	}
	sid := wire.SessionID{13}
	a.client.Register(sid)
	b.client.Register(sid)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("a and b paired", func() bool { return a.client.PeerPresent(sid) && b.client.PeerPresent(sid) })
	c.client.Register(sid)
	waitFor("reject", func() bool { code, _ := c.client.Rejection(sid); return code == wire.RejectSessionFull })
	if c.client.PeerPresent(sid) {
		t.Fatal("a rejected session reports the peer as present")
	}
	c.client.mu.Lock()
	due := time.Until(c.client.sessions[sid].lastSent.Add(opts.RegisterInterval))
	c.client.mu.Unlock()
	if due > fullRetry || due < fullRetry-2*time.Second {
		t.Fatalf("next register in %v, want about %v", due, fullRetry)
	}
	// Repeated rejects with the same code are not reported again.
	c.client.Refresh(sid)
	time.Sleep(200 * time.Millisecond)
	c.client.Refresh(sid)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	n := len(rejects)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("OnReject called %d times, want 1", n)
	}
	// b leaves; c gets in and the reject is cleared.
	b.client.Unregister(sid)
	c.client.Refresh(sid)
	waitFor("c admitted", func() bool { return c.client.PeerPresent(sid) })
	if code, _ := c.client.Rejection(sid); code != 0 {
		t.Fatalf("reject %d still recorded after the Ack", code)
	}
}
