package engine

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/discovery"
	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/relay"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/transport"
	"github.com/NotrinoResearch/lanfolk/internal/tunwrap"
	"github.com/NotrinoResearch/lanfolk/internal/winnet"
	"golang.zx2c4.com/wireguard/tun"
)

type testRelay struct {
	srv    *relay.Server
	cancel context.CancelFunc
	done   chan struct{}
}

func startTestRelay(t *testing.T) *testRelay {
	cfg := relay.DefaultConfig()
	cfg.UDPAddr, cfg.TCPAddr, cfg.MetricsAddr = "127.0.0.1:0", "127.0.0.1:0", ""
	s, err := relay.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &testRelay{srv: s, cancel: cancel, done: make(chan struct{})}
	go func() { s.Serve(ctx); close(r.done) }()
	t.Cleanup(r.stop)
	return r
}

func (r *testRelay) stop() {
	r.cancel()
	<-r.done
}

type node struct {
	t      *testing.T
	name   string
	dir    string
	e      *Engine
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	tun    *tunwrap.MemTUN
}

func newNode(t *testing.T, name string, r *testRelay) *node {
	t.Helper()
	return newNodeIn(t, name, r, t.TempDir(), nil)
}

// newNodeIn starts a node on an existing data directory (restarts).
func newNodeIn(t *testing.T, name string, r *testRelay, dir string, mod func(*Config)) *node {
	t.Helper()
	n := &node{t: t, name: name, dir: dir, done: make(chan struct{})}
	level := slog.LevelWarn
	if os.Getenv("LANFOLK_TEST_LOG") != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})).With("node", name)
	cfg := Config{
		DataDir:    dir,
		Logger:     log,
		ListenAddr: "127.0.0.1:0",
		NewTUN: func(string, int) (tun.Device, error) {
			m := tunwrap.NewMemTUN(name, MTU)
			n.mu.Lock()
			n.tun = m
			n.mu.Unlock()
			return m, nil
		},
		Blocked: winnet.IsBlocked,
		DefaultRelays: []store.Relay{{Host: "127.0.0.1", UDPPort: int(r.srv.UDPAddr().Port()),
			TCPPort: r.srv.TCPAddr().(*net.TCPAddr).Port}},
		LoopbackCandidates: true,
		DisablePortMapping: true,
		DisableDHT:         true,
		HelloInterval:      500 * time.Millisecond,
		OfflineAfter:       3 * time.Second,
		RelayOptions: func(o *transport.RelayOptions) {
			o.PingInterval = 200 * time.Millisecond
			o.DownAfter = time.Second
			o.RegisterInterval = time.Second
		},
	}
	if mod != nil {
		mod(&cfg)
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.e = e
	if err := e.SetNickname(name); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	go func() { e.Run(ctx); close(n.done) }()
	t.Cleanup(n.stop)
	return n
}

func (n *node) stop() {
	n.cancel()
	<-n.done
}

// stopOnce stops a node that may already have been stopped.
func (n *node) stopped() bool {
	select {
	case <-n.done:
		return true
	default:
		return false
	}
}

func (n *node) code() string { return n.e.PublicKey().FriendCode() }

func (n *node) memtun() *tunwrap.MemTUN {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tun
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

func friendState(n *node, other *node) string {
	for _, f := range n.e.Friends() {
		if f.PublicKey == other.e.PublicKey().Hex() {
			return f.State
		}
	}
	return ""
}

func befriend(t *testing.T, a, b *node) {
	t.Helper()
	if _, err := a.e.AddFriend(b.code(), b.name); err != nil {
		t.Fatal(err)
	}
	if _, err := b.e.AddFriend(a.code(), a.name); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "mutual friendship", func() bool {
		return friendState(a, b) == "active" && friendState(b, a) == "active"
	})
}

func (n *node) vip() netip.Addr {
	ip, _ := netip.ParseAddr(n.e.Status().VirtualIP)
	return ip
}

func (n *node) peer(other *node) ipc.Peer {
	for _, p := range n.e.Peers() {
		if p.PublicKey == other.e.PublicKey().Hex() {
			return p
		}
	}
	return ipc.Peer{}
}

// send injects a UDP packet into n's adapter, as an application would.
func (n *node) send(dst netip.AddrPort, payload []byte) {
	src := netip.AddrPortFrom(n.vip(), 40000)
	n.memtun().InjectFromOS(tunwrap.BuildUDP4(src, dst, 64, payload))
}

// expect waits for a UDP packet with payload on n's adapter.
func (n *node) expect(t *testing.T, d time.Duration, payload []byte) (src, dst netip.AddrPort) {
	t.Helper()
	timeout := time.After(d)
	for {
		m := n.memtun()
		if m == nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		select {
		case pkt := <-m.ToOS():
			s, ds, p, ok := tunwrap.ParseUDP4(pkt)
			if ok && bytes.Equal(p, payload) {
				return s, ds
			}
		case <-timeout:
			t.Fatalf("%s: packet %q not delivered within %v", n.name, payload, d)
			return
		}
	}
}

func (n *node) expectNone(t *testing.T, d time.Duration, payload []byte) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case pkt := <-n.memtun().ToOS():
			if _, _, p, ok := tunwrap.ParseUDP4(pkt); ok && bytes.Equal(p, payload) {
				t.Fatalf("%s: packet %q should have been dropped", n.name, payload)
			}
		case <-timeout:
			return
		}
	}
}

// sendUntil repeats a packet until it arrives, for the first packet that
// also waits for the WireGuard handshake.
func sendUntil(t *testing.T, from, to *node, port uint16, payload []byte) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		from.send(netip.AddrPortFrom(to.vip(), port), payload)
		select {
		case pkt := <-to.memtun().ToOS():
			if _, _, p, ok := tunwrap.ParseUDP4(pkt); ok && bytes.Equal(p, payload) {
				return
			}
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Fatalf("%s -> %s: %q never delivered", from.name, to.name, payload)
}

func createNetwork(t *testing.T, owner *node, members ...*node) string {
	t.Helper()
	l, err := owner.e.CreateNetwork("Friday games")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if err := owner.e.AddMember(l.ID, m.e.PublicKey()); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range members {
		m := m
		waitFor(t, 10*time.Second, m.name+" joins the network", func() bool {
			st := m.e.Status()
			return st.ActiveNetworkID == l.ID.String() && st.AdapterUp && len(m.e.Peers()) == len(members)
		})
	}
	return l.ID.String()
}

func TestFriendshipNeedsBothSides(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	if _, err := a.e.AddFriend(b.code(), "Mehmet"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if s := friendState(a, b); s != "pending" {
		t.Fatalf("one-sided friendship is %q", s)
	}
	if _, err := b.e.AddFriend(a.code(), "Ahmet"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "friendship to become active on both sides", func() bool {
		return friendState(a, b) == "active" && friendState(b, a) == "active"
	})
	waitFor(t, 5*time.Second, "presence", func() bool {
		for _, f := range a.e.Friends() {
			if f.Online && f.Nickname != "" {
				return true
			}
		}
		return false
	})
	// Mismatched or broken codes are rejected.
	if _, err := a.e.AddFriend(b.code()[:20], ""); err == nil {
		t.Fatal("truncated code accepted")
	}
	if _, err := a.e.AddFriend(a.code(), ""); err == nil {
		t.Fatal("own code accepted")
	}
}

// memberName returns the nickname n shows for other's row in its first
// network.
func memberName(n, other *node) string {
	nets := n.e.Networks()
	if len(nets) == 0 {
		return ""
	}
	for _, m := range nets[0].Members {
		if m.PublicKey == other.e.PublicKey().Hex() {
			return m.Nickname
		}
	}
	return ""
}

// A renamed member shows its new nickname in member and friend lists on
// both sides, although the signed member list keeps the old one
// (2026-11-04 field test).
func TestRenameShowsInMemberAndFriendLists(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	if err := a.e.SetNickname("osman"); err != nil {
		t.Fatal(err)
	}
	if err := b.e.SetNickname("caan"); err != nil {
		t.Fatal(err)
	}
	if got := memberName(a, a); got != "osman" {
		t.Fatalf("own row shows %q", got)
	}
	waitFor(t, 10*time.Second, "new nicknames in member lists", func() bool {
		return memberName(b, a) == "osman" && memberName(a, b) == "caan" && memberName(b, b) == "caan"
	})
	waitFor(t, 5*time.Second, "new nicknames in friend lists", func() bool {
		fa, fb := a.e.Friends(), b.e.Friends()
		return len(fa) == 1 && fa[0].Nickname == "caan" && len(fb) == 1 && fb[0].Nickname == "osman"
	})
}

// 5 November: after the service restarted, an offline friend who had renamed
// himself showed the name he had when he joined ("Player-241D"), because the
// announced nickname lived only in memory.
func TestRenamedMemberKeepsNameAfterRestart(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	if err := b.e.SetNickname("osman"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "new nickname at a", func() bool { return memberName(a, b) == "osman" })
	b.stop()
	a.stop()

	a2 := newNodeIn(t, "ahmet", r, a.dir, nil) // b stays offline
	if got := memberName(a2, b); got != "osman" {
		t.Fatalf("member list shows %q after a restart", got)
	}
	f := a2.e.Friends()
	if len(f) != 1 || f[0].Nickname != "osman" {
		t.Fatalf("friend list shows %+v after a restart", f)
	}
}

func TestNetworkTunnelDirectPath(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	if a.vip().String() != "10.77.0.2" || b.vip().String() != "10.77.0.3" {
		t.Fatalf("addresses %v %v", a.vip(), b.vip())
	}
	sendUntil(t, a, b, 9000, []byte("hello over the tunnel"))
	sendUntil(t, b, a, 9000, []byte("and back"))
	waitFor(t, 10*time.Second, "direct path", func() bool { return a.peer(b).Path == "direct" && b.peer(a).Path == "direct" })
	p := a.peer(b)
	if p.Quality == "unknown" || p.AvgPingMs <= 0 {
		t.Fatalf("no live stats: %+v", p)
	}
	src, _ := b.expectAfterSend(t, a, 9001, []byte("source check"))
	if src.Addr() != a.vip() {
		t.Fatalf("source %v", src)
	}
}

func (n *node) expectAfterSend(t *testing.T, from *node, port uint16, payload []byte) (netip.AddrPort, netip.AddrPort) {
	from.send(netip.AddrPortFrom(n.vip(), port), payload)
	return n.expect(t, 5*time.Second, payload)
}

func blockDirect(a, b *node) {
	pa, pb := a.e.Port(), b.e.Port()
	a.e.Socket().SetFilter(func(x netip.AddrPort) bool { return x.Port() == pb })
	b.e.Socket().SetFilter(func(x netip.AddrPort) bool { return x.Port() == pa })
}

func TestSharedRelayWhenDirectIsBlocked(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	blockDirect(a, b)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("via relay"))
	if p := a.peer(b).Path; p != "shared-relay" {
		t.Fatalf("path %q", p)
	}
	time.Sleep(4 * time.Second) // probing has given up on the direct candidates
	if p := a.peer(b).Path; p != "shared-relay" {
		t.Fatalf("path changed to %q although direct is blocked", p)
	}
}

// Relay only on one side must move both directions to the relay and leave
// the home address unused: the member that still knows it neither probes it
// nor gets answers from it (SETUP.md: members see only the relay's address).
func TestRelayOnlyOnOneSideStopsDirectTraffic(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("hello"))
	waitFor(t, 10*time.Second, "direct path", func() bool { return a.peer(b).Path == "direct" && b.peer(a).Path == "direct" })

	s := b.e.Settings()
	s.RelayOnly = true
	if _, err := b.e.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "relay on both sides", func() bool {
		return a.peer(b).Path == "shared-relay" && b.peer(a).Path == "shared-relay"
	})
	var direct atomic.Int64
	pa := a.e.Port()
	b.e.Socket().SetFilter(func(x netip.AddrPort) bool {
		if x.Port() == pa {
			direct.Add(1)
		}
		return false
	})
	time.Sleep(3 * time.Second)
	if n := direct.Load(); n != 0 {
		t.Fatalf("%d packets between the home addresses after Relay only", n)
	}
	sendUntil(t, a, b, 9001, []byte("over the relay"))
	sendUntil(t, b, a, 9001, []byte("and back"))

	s.RelayOnly = false
	if _, err := b.e.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "direct path again", func() bool { return a.peer(b).Path == "direct" && b.peer(a).Path == "direct" })
}

// Two-home matrix, 5 November: twice the direct path came back only after
// 7.5 minutes, the sum of the 30 s, 60 s, 120 s and 240 s retries. Once a
// direct path had failed, a later Candidates message from the peer (sent on
// a settings change and every minute) did not restart it, and hole punching
// through two NATs needs both sides to probe at the same time.
func TestPeerCandidatesRestartFailedDirectPaths(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("hello"))
	waitFor(t, 10*time.Second, "direct path", func() bool { return a.peer(b).Path == "direct" && b.peer(a).Path == "direct" })

	blockDirect(a, b) // the direct path dies; both sides fall back and back off
	waitFor(t, 10*time.Second, "relay on both sides", func() bool {
		return a.peer(b).Path == "shared-relay" && b.peer(a).Path == "shared-relay"
	})
	// A standby path is failed after 16 s of silence (StandbyDeadAfter); then
	// its 3 s burst ends and the 30 s retry is pending.
	time.Sleep(22 * time.Second)
	a.e.Socket().SetFilter(nil)
	b.e.Socket().SetFilter(nil)

	// b sends its candidates, as it does after a settings change or once a
	// minute. Both sides must probe at once instead of waiting 30 s.
	pa := b.e.peerByKey(a.e.PublicKey())
	pa.mu.Lock()
	pa.candSentAt = time.Time{}
	pa.mu.Unlock()
	b.e.maybeSendCandidates(pa, time.Now())
	waitFor(t, 6*time.Second, "direct path again", func() bool { return a.peer(b).Path == "direct" && b.peer(a).Path == "direct" })
}

func TestBroadcastAndBlockedPorts(t *testing.T) {
	r := startTestRelay(t)
	a, b, c := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "veli", r)
	befriend(t, a, b)
	befriend(t, a, c)
	createNetwork(t, a, b, c)
	sendUntil(t, a, b, 9000, []byte("warm up b"))
	sendUntil(t, a, c, 9000, []byte("warm up c"))
	// Subnet broadcast reaches every member with the sender's address.
	t.Cleanup(func() {
		if t.Failed() {
			for _, n := range []*node{a, b, c} {
				t.Logf("%s lan stats: %+v vips=%v", n.name, n.lanStats(), n.e.onlinePeerVIPs())
			}
		}
	})
	// Retries queued during the handshake may still sit in the adapters.
	time.Sleep(300 * time.Millisecond)
	b.drain()
	c.drain()
	a.send(netip.MustParseAddrPort("10.77.0.255:27015"), []byte("lan discovery"))
	for _, n := range []*node{b, c} {
		src, dst := n.expect(t, 5*time.Second, []byte("lan discovery"))
		if src.Addr() != a.vip() || dst.Addr() != netip.MustParseAddr("10.77.0.255") {
			t.Fatalf("%s got %v -> %v", n.name, src, dst)
		}
	}
	a.send(netip.MustParseAddrPort("255.255.255.255:27015"), []byte("global broadcast"))
	b.expect(t, 5*time.Second, []byte("global broadcast"))
	a.send(netip.MustParseAddrPort("239.255.77.77:7793"), []byte("multicast"))
	c.expect(t, 5*time.Second, []byte("multicast"))
	// NetBIOS (UDP 137) to a member is blocked.
	a.send(netip.AddrPortFrom(b.vip(), 137), []byte("netbios"))
	b.expectNone(t, time.Second, []byte("netbios"))
}

func TestOutsiderCannotJoin(t *testing.T) {
	r := startTestRelay(t)
	a, b, d := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "stranger", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	// The stranger knows Ahmet's code but Ahmet never added the stranger.
	if _, err := d.e.AddFriend(a.code(), ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if len(d.e.Networks()) != 0 || friendState(d, a) != "pending" {
		t.Fatal("stranger learned about the network or became a friend")
	}
	l := a.e.Networks()[0]
	if err := a.e.AddMember(mustNetID(t, l.ID), d.e.PublicKey()); err == nil {
		t.Fatal("owner added a non-friend")
	}
	for _, p := range a.e.Peers() {
		if p.PublicKey == d.e.PublicKey().Hex() {
			t.Fatal("stranger is a tunnel peer")
		}
	}
}

func mustNetID(t *testing.T, s string) [16]byte {
	id, err := parseNetID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRemovedMemberLosesTunnel(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	id := createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("before removal"))
	if err := a.e.RemoveMember(mustNetID(t, id), b.e.PublicKey()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "removed member to drop the network", func() bool {
		return len(b.e.Networks()) == 0 && b.e.Status().ActiveNetworkID == ""
	})
	if len(a.e.Peers()) != 0 {
		t.Fatal("owner still has a tunnel peer for the removed member")
	}
}

func TestOwnerOfflineMembersStillConnect(t *testing.T) {
	r := startTestRelay(t)
	a, b, c := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "veli", r)
	befriend(t, a, b)
	befriend(t, a, c)
	createNetwork(t, a, b, c)
	a.stop()
	// Mehmet and Veli are not friends, but both have the owner-signed list.
	sendUntil(t, b, c, 9000, []byte("owner is offline"))
	if _, _, err := b.e.CreateConnCode(c.e.PublicKey()); err != nil {
		t.Fatalf("connection code for a co-member: %v", err)
	}
}

func TestMemberRelayWhenSharedRelayIsDown(t *testing.T) {
	r := startTestRelay(t)
	a, b, c := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "veli", r)
	blockDirect(b, c)
	befriend(t, a, b)
	befriend(t, a, c)
	createNetwork(t, a, b, c)
	sendUntil(t, b, c, 9000, []byte("via shared relay"))
	waitFor(t, 10*time.Second, "direct paths to the owner", func() bool {
		return b.peer(a).Path == "direct" && c.peer(a).Path == "direct"
	})
	// Give the owner time to send relay offers, then take the relay down.
	time.Sleep(6 * time.Second)
	r.stop()
	waitFor(t, 15*time.Second, "member relay path", func() bool {
		return b.peer(c).Path == "member-relay" && c.peer(b).Path == "member-relay"
	})
	if d := b.peer(c).RelayDetail; d != "ahmet" {
		t.Fatalf("relay detail %q", d)
	}
	sendUntil(t, b, c, 9001, []byte("via ahmet"))
}

func TestConnectionCodeRoundTrip(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	code, exp, err := a.e.CreateConnCode(b.e.PublicKey())
	if err != nil || time.Until(exp) < 9*time.Minute {
		t.Fatalf("create: %v", err)
	}
	pk, err := b.e.UseConnCode(code)
	if err != nil || pk != a.e.PublicKey() {
		t.Fatalf("use: %v", err)
	}
	if _, err := a.e.UseConnCode(code); err == nil {
		t.Fatal("a code was accepted by its own creator")
	}
	var zero identity.PublicKey
	if _, _, err := a.e.CreateConnCode(zero); err == nil {
		t.Fatal("code for unknown peer")
	}
}

func TestIPCHandlerValidates(t *testing.T) {
	r := startTestRelay(t)
	a := newNode(t, "ahmet", r)
	ctx := context.Background()
	if _, err := a.e.Handle(ctx, ipc.MethodAddFriend, []byte(`{"code":"lanfolk1garbage"}`)); err == nil {
		t.Fatal("invalid code accepted over IPC")
	}
	if _, err := a.e.Handle(ctx, ipc.MethodSetSettings, []byte(`{"manual_port":70000}`)); err == nil {
		t.Fatal("invalid port accepted")
	}
	if _, err := a.e.Handle(ctx, "no.such.method", nil); err == nil {
		t.Fatal("unknown method accepted")
	}
	if _, err := a.e.Handle(ctx, ipc.MethodSetNickname, []byte(`{"nickname":"Ahmet"}`)); err != nil {
		t.Fatal(err)
	}
	if a.e.Status().Nickname != "Ahmet" {
		t.Fatal("nickname not applied")
	}
}

func (n *node) lanStats() tunwrap.Stats {
	n.e.mu.Lock()
	defer n.e.mu.Unlock()
	if n.e.tunnel == nil {
		return tunwrap.Stats{}
	}
	return n.e.tunnel.wrap.Stats()
}

// drain discards packets already delivered to n's adapter.
func (n *node) drain() {
	for {
		select {
		case <-n.memtun().ToOS():
		default:
			return
		}
	}
}

func startPrivateDHT(t *testing.T, n int) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var nodes []*discovery.DHT
	for i := 0; i < n; i++ {
		opts := discovery.DHTOptions{Addr: "127.0.0.1:0", NoBootstrapDefaults: true, Serve: true}
		if i > 0 {
			opts.Bootstrap = []string{nodes[0].Addr().String()}
		}
		d, err := discovery.StartDHT(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		<-d.Ready()
		nodes = append(nodes, d)
	}
	if err := discovery.LinkLocalNetwork(nodes); err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, d := range nodes {
		addrs = append(addrs, d.Addr().String())
	}
	return addrs
}

// The Phase 6 gate: with the shared relay down, the DHT reconnects members.
// After a restart both members listen on new ports, so their cached
// addresses are stale and only the DHT can tell them where to go.
func TestDHTReconnectsWhenRelayIsDown(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("before the outage"))
	a.stop()
	b.stop()
	r.stop()

	bootstrap := startPrivateDHT(t, 5)
	withDHT := func(c *Config) {
		c.DisableDHT = false
		c.DHTBootstrap = bootstrap
		c.DHTAfter = time.Second
	}
	a2 := newNodeIn(t, "ahmet", r, a.dir, withDHT)
	b2 := newNodeIn(t, "mehmet", r, b.dir, withDHT)
	waitFor(t, 20*time.Second, "DHT fallback to start", func() bool {
		return a2.e.Status().DHT == "running" && b2.e.Status().DHT == "running"
	})
	waitFor(t, 60*time.Second, "direct path found through the DHT", func() bool {
		return a2.peer(b2).Path == "direct" && b2.peer(a2).Path == "direct"
	})
	sendUntil(t, a2, b2, 9001, []byte("found through the DHT"))
	if !hasSource(a2, b2, pathmgr.SourceDHT) && !hasSource(b2, a2, pathmgr.SourceDHT) {
		t.Fatal("the direct path was not learned from the DHT")
	}
}

func hasSource(n, other *node, src pathmgr.Source) bool {
	p := n.e.peerByKey(other.e.PublicKey())
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pm == nil {
		return false
	}
	for _, pi := range p.pm.Paths(0) {
		if pi.Source == src {
			return true
		}
	}
	return false
}

// An offline member keeps an optimistic relay path for its first packets;
// the API must not report that as a connection.
func TestOfflineMemberShowsNoPath(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("hello"))
	b.stop()
	waitFor(t, 15*time.Second, "member offline", func() bool { return !a.peer(b).Online })
	if p := a.peer(b); p.Path != "none" || p.RelayDetail != "" {
		t.Fatalf("offline member reported as connected: %+v", p)
	}
}

// Members cannot reach a port that only a Windows service owns, unless the
// user allows it in the troubleshooting settings.
func TestServicePortsUnreachableUntilAllowed(t *testing.T) {
	r := startTestRelay(t)
	a := newNode(t, "ahmet", r)
	b := newNodeIn(t, "mehmet", r, t.TempDir(), func(c *Config) {
		c.ServicePort = func(proto uint8, port uint16) bool { return proto == 17 && port == 7000 }
	})
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 9000, []byte("game traffic"))
	a.send(netip.AddrPortFrom(b.vip(), 7000), []byte("to a service"))
	b.expectNone(t, time.Second, []byte("to a service"))
	if s := b.lanStats(); s.DroppedService == 0 {
		t.Fatalf("no service drop counted: %+v", s)
	}
	if !strings.Contains(b.e.Diagnostics(), `"DroppedService": `) {
		t.Error("diagnostics lack the tunnel counters")
	}

	st := b.e.Settings()
	st.AllowServices = true
	if _, err := b.e.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	if !b.e.Settings().AllowServices {
		t.Fatal("setting not stored")
	}
	sendUntil(t, a, b, 7000, []byte("allowed now"))
}
