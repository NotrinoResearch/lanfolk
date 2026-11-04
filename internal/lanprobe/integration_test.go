package lanprobe

import (
	"context"
	"net"
	"testing"
	"time"
)

var anyPorts = Ports{TCP: -1, Unicast: -1, Broadcast: -1, Multicast: -1, Latency: -1}

func startServer(t *testing.T, iface string) *Server {
	t.Helper()
	srv, err := NewServer(ServerConfig{Bind: "127.0.0.1", IfaceIP: iface, Ports: anyPorts, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return srv
}

func byID(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func TestLoopbackUnicastAndIPInPayload(t *testing.T) {
	srv := startServer(t, "")
	rs, err := RunTest(context.Background(), TestConfig{
		Peer: "127.0.0.1", Local: "127.0.0.1", Ports: srv.Ports(),
		Skip: map[string]bool{IDSubnetBcast: true, IDGlobalBound: true, IDGlobalAny: true, IDMcastIF: true,
			IDMcastDefault: true, IDBlockedPorts: true},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := byID(rs)
	WriteResultsTo(t, rs)
	for _, id := range []string{IDTCP, IDUDP, IDIPInPayload} {
		if m[id].Status != Pass {
			t.Errorf("%s: %+v", id, m[id])
		}
	}
	if m[IDPrivateRange].Status != Fail {
		t.Errorf("127.0.0.1 is not RFC1918, got %+v", m[IDPrivateRange])
	}
	if m[IDSubnetBcast].Status != Skip {
		t.Errorf("skip not honoured: %+v", m[IDSubnetBcast])
	}
	if !Failed(rs) {
		t.Error("a failing core test (private range) must make Failed true")
	}
}

func TestLoopbackIPInPayloadMismatch(t *testing.T) {
	srv := startServer(t, "")
	// The tester claims 10.77.0.2 but the packets come from 127.0.0.1.
	rs, err := RunTest(context.Background(), TestConfig{
		Peer: "127.0.0.1", Local: "10.77.0.2", Ports: srv.Ports(),
		Skip: map[string]bool{IDSubnetBcast: true, IDGlobalBound: true, IDGlobalAny: true, IDMcastIF: true,
			IDMcastDefault: true, IDBlockedPorts: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := byID(rs)[IDIPInPayload]
	if r.Status != Fail {
		t.Fatalf("mismatch must fail: %+v", r)
	}
	t.Log(r.Detail)
}

func TestTesterWithoutServer(t *testing.T) {
	// Grab free ports, then close them so nothing listens there.
	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	tcpPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rs, err := RunTest(context.Background(), TestConfig{
		Peer: "127.0.0.1", Local: "127.0.0.1",
		Ports:       Ports{TCP: tcpPort, Unicast: tcpPort, Broadcast: tcpPort, Multicast: tcpPort, Latency: tcpPort},
		Skip:        map[string]bool{IDBlockedPorts: true, IDUDP: true},
		DialTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := byID(rs)
	if m[IDTCP].Status != Fail || m[IDSubnetBcast].Status != Fail || m[IDIPInPayload].Status != Fail {
		t.Errorf("everything that needs the server must fail: %+v", rs)
	}
}

func TestBlockedPorts(t *testing.T) {
	open, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	closed, _ := net.Listen("tcp4", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	openPort := open.Addr().(*net.TCPAddr).Port

	ctx := context.Background()
	peer := mustAddr("127.0.0.1")
	if r := testBlockedPorts(ctx, peer, []int{closedPort}, time.Second); r.Status != Pass {
		t.Errorf("unreachable port must PASS: %+v", r)
	}
	if r := testBlockedPorts(ctx, peer, []int{closedPort, openPort}, time.Second); r.Status != Fail {
		t.Errorf("reachable port must FAIL: %+v", r)
	}
}

// TestLoopbackBroadcastMulticastBestEffort exercises the broadcast and
// multicast code paths. Loopback does not behave the same on every OS, so it
// only logs the outcome.
func TestLoopbackBroadcastMulticastBestEffort(t *testing.T) {
	srv, err := NewServer(ServerConfig{Bind: "127.0.0.1", IfaceIP: "127.0.0.1", Ports: anyPorts, Logf: t.Logf})
	if err != nil {
		t.Skipf("cannot join the multicast group on loopback here: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	rs, err := RunTest(ctx, TestConfig{
		Peer: "127.0.0.1", Local: "127.0.0.1", Ports: srv.Ports(), SubnetBroadcast: "127.255.255.255",
		Skip: map[string]bool{IDBlockedPorts: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	WriteResultsTo(t, rs)
}

func TestLatencyLoopback(t *testing.T) {
	srv := startServer(t, "")
	s, err := RunLatency(context.Background(), LatencyConfig{
		Peer: "127.0.0.1", Port: srv.Ports().Latency, Count: 300, Size: 200, Rate: 3000, Linger: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Sent != 300 || s.Received < 285 {
		t.Fatalf("loopback must not lose packets: %+v", s)
	}
	if s.Min <= 0 || s.P50 > 50*time.Millisecond || s.Min > s.P50 || s.P50 > s.P99 || s.P99 > s.Max {
		t.Fatalf("implausible summary: %+v", s)
	}
	t.Logf("loopback: min %s p50 %s p99 %s max %s", Ms(s.Min), Ms(s.P50), Ms(s.P99), Ms(s.Max))
}

func TestLatencyCompareLoopback(t *testing.T) {
	srv := startServer(t, "")
	cfg := LatencyConfig{Peer: "127.0.0.1", Port: srv.Ports().Latency, Count: 200, Size: 64, Rate: 2000}
	raw, tun, err := RunLatencyCompare(context.Background(), cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Received < 190 || tun.Received < 190 {
		t.Fatalf("raw %+v tunnel %+v", raw, tun)
	}
}

func TestLatencyRejectsBadConfig(t *testing.T) {
	if _, err := RunLatency(context.Background(), LatencyConfig{Peer: "127.0.0.1", Size: 5}); err == nil {
		t.Error("size below header must fail")
	}
	if _, err := RunLatency(context.Background(), LatencyConfig{Peer: "not-an-ip"}); err == nil {
		t.Error("bad peer must fail")
	}
}
