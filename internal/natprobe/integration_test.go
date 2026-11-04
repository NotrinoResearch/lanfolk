package natprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2"

	"github.com/NotrinoResearch/lanfolk/internal/relay"
)

func startRelay(t *testing.T) *relay.Server {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.UDPAddr = "127.0.0.1:0"
	cfg.TCPAddr = "127.0.0.1:0"
	cfg.MetricsAddr = ""
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

func tcpPort(s *relay.Server) int { return s.TCPAddr().(*net.TCPAddr).Port }

func TestRelayRTT(t *testing.T) {
	s := startRelay(t)
	sc := &scrubber{}
	ctx := context.Background()

	u, err := PingUDP(ctx, s.UDPAddr(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Sent != pingSamples || u.Received != pingSamples || u.MinMs <= 0 || u.MinMs > u.MedianMs || u.MedianMs > u.P95Ms || u.P95Ms > 100 {
		t.Errorf("udp stats %+v", u)
	}

	tcpAddr := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", tcpPort(s)))
	tc, conMs, err := PingTCP(ctx, tcpAddr, sc)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Received != pingSamples || conMs <= 0 || tc.MinMs <= 0 || tc.P95Ms > 100 {
		t.Errorf("tcp stats %+v connect %v", tc, conMs)
	}
	t.Logf("udp min/med/p95 %.3f/%.3f/%.3f ms, tcp %.3f/%.3f/%.3f ms, connect %.3f ms",
		u.MinMs, u.MedianMs, u.P95Ms, tc.MinMs, tc.MedianMs, tc.P95Ms, conMs)
}

func TestRelayUnreachable(t *testing.T) {
	l, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dead := l.LocalAddr().(*net.UDPAddr).AddrPort() // nobody answers
	defer l.Close()
	start := time.Now()
	_, err := PingUDP(context.Background(), dead, &scrubber{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("unreachable relay must fail fast, took %v", d)
	}
}

func TestMeasureTimeoutsShortDelays(t *testing.T) {
	s := startRelay(t)
	start := time.Now()
	res := MeasureTimeouts(context.Background(), s.UDPAddr(), []int{1, 2}, &scrubber{})
	for _, r := range res {
		if r.Err != "" || !r.Survived {
			t.Errorf("%+v", r)
		}
		if r.ArrivedMs < float64(r.Seconds)*1000-300 || r.ArrivedMs > float64(r.Seconds)*1000+1500 {
			t.Errorf("reply for %d s arrived after %.0f ms", r.Seconds, r.ArrivedMs)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("sockets must run in parallel: took %v", d)
	}
	if rec := RecommendKeepalive(res); rec.LargestSurvived != 2 || rec.IntervalSec != 1 {
		t.Errorf("recommendation %+v", rec)
	}
}

func TestMeasureTimeoutsNoReply(t *testing.T) {
	// A target that never answers: the result is "expired", without error.
	l, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer l.Close()
	res := MeasureTimeouts(context.Background(), l.LocalAddr().(*net.UDPAddr).AddrPort(), []int{1}, &scrubber{})
	if res[0].Survived || res[0].Err != "" {
		t.Errorf("%+v", res[0])
	}
}

func TestMeasureTimeoutsRejectsBadValues(t *testing.T) {
	res := MeasureTimeouts(context.Background(), netip.MustParseAddrPort("127.0.0.1:9"), []int{300}, &scrubber{})
	if res[0].Err == "" {
		t.Errorf("300 s does not fit the protocol byte: %+v", res[0])
	}
}

func TestRunAgainstInProcessRelay(t *testing.T) {
	s := startRelay(t)
	s2 := startRelay(t) // a second "STUN server" (same IP, other port)
	var logs []string
	rep := Run(context.Background(), Config{
		Relay:        "localhost",
		RelayUDPPort: int(s.UDPAddr().Port()),
		RelayTCPPort: tcpPort(s),
		STUN:         []string{fmt.Sprintf("127.0.0.1:%d", s2.UDPAddr().Port())},
		Timeouts:     []int{1, 2},
		SkipDHT:      true,
		SkipIPv6:     true,
		ISP:          "Test|ISP",
		Logf:         func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	})

	if rep.Mapping.Class.Behavior != MappingEasy || rep.Mapping.Class.PortPreserved != "yes" || rep.Mapping.Class.Answers != 2 {
		t.Errorf("mapping %+v", rep.Mapping.Class)
	}
	if rep.Second == nil || rep.Second.Allocation.Pattern != "preserved" {
		t.Errorf("second socket %+v", rep.Second)
	}
	if rep.Relay.UDP == nil || rep.Relay.TCP == nil || rep.Relay.UDPErr != "" || rep.Relay.TCPErr != "" {
		t.Errorf("relay %+v", rep.Relay)
	}
	if rep.Timeout.Skipped != "" || len(rep.Timeout.Results) != 2 || !rep.Timeout.Results[1].Survived || rep.Timeout.Keepalive.IntervalSec != 1 {
		t.Errorf("timeout %+v", rep.Timeout)
	}
	if rep.CGNAT.BehindNAT != "no" {
		t.Errorf("loopback address is on an interface: %+v", rep.CGNAT)
	}

	// The relay host must never appear in any output.
	var sum strings.Builder
	rep.WriteSummary(&sum)
	js, _ := json.Marshal(rep)
	row := rep.MarkdownRow()
	for name, out := range map[string]string{"summary": sum.String(), "json": string(js), "markdown": row, "logs": strings.Join(logs, "\n")} {
		if strings.Contains(strings.ToLower(out), "localhost") {
			t.Errorf("%s leaks the relay host:\n%s", name, out)
		}
	}
	if strings.Contains(row, "127.0.0.1") {
		t.Errorf("markdown row must not contain IPs: %s", row)
	}
	if !strings.HasPrefix(row, "| Test/ISP | easy | yes, ") && !strings.HasPrefix(row, "| Test/ISP | easy | yes |") {
		t.Errorf("row %q", row)
	}
	t.Logf("\n%s\n%s\n%s", sum.String(), MarkdownHeader, row)
}

func TestRunScrubsErrors(t *testing.T) {
	s := startRelay(t)
	// A TCP port nobody listens on: the dial error would normally include
	// the relay address.
	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	closedPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	rep := Run(context.Background(), Config{
		Relay: "localhost", RelayUDPPort: int(s.UDPAddr().Port()), RelayTCPPort: closedPort,
		STUN: []string{fmt.Sprintf("127.0.0.1:%d", s.UDPAddr().Port())}, Timeouts: []int{}, SkipDHT: true, SkipIPv6: true,
	})
	if rep.Relay.TCPErr == "" {
		t.Fatal("expected a TCP error")
	}
	if strings.Contains(rep.Relay.TCPErr, "127.0.0.1") || strings.Contains(strings.ToLower(rep.Relay.TCPErr), "localhost") {
		t.Errorf("TCP error leaks the relay address: %q", rep.Relay.TCPErr)
	}
	if rep.Timeout.Skipped == "" {
		t.Errorf("no timeouts requested should be reported as skipped: %+v", rep.Timeout)
	}
}

func TestRunWithoutRelay(t *testing.T) {
	// No relay, and an unresolvable STUN server: failures are reported, not fatal.
	rep := Run(context.Background(), Config{STUN: []string{"nonexistent.invalid:3478"}, SkipDHT: true, SkipIPv6: true})
	if rep.Mapping.Class.Behavior != MappingUnknown || rep.Timeout.Skipped == "" || rep.Relay.Configured {
		t.Errorf("%+v", rep)
	}
	var sb strings.Builder
	rep.WriteSummary(&sb)
	if rep.MarkdownRow() == "" || sb.Len() == 0 {
		t.Error("empty rendering")
	}
}

func TestDHTLocalCluster(t *testing.T) {
	// A small DHT on loopback: the node count and the BEP 44 put/get logic
	// are exercised without touching the internet.
	var boot string
	var servers []*dht.Server
	for i := 0; i < 6; i++ {
		cfg := dht.NewDefaultServerConfig()
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Conn = conn
		if boot != "" {
			b := boot
			cfg.StartingNodes = func() ([]dht.Addr, error) { return dht.ResolveHostPorts([]string{b}) }
		} else {
			cfg.StartingNodes = func() ([]dht.Addr, error) { return nil, nil }
		}
		s, err := dht.NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		servers = append(servers, s)
		if boot == "" {
			boot = conn.LocalAddr().String()
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.BootstrapContext(ctx)
			cancel()
		}
	}
	t.Cleanup(func() {
		for _, s := range servers {
			s.Close()
		}
	})
	r := MeasureDHT(context.Background(), nil, 10*time.Second, []string{boot})
	if r.Nodes == 0 {
		t.Fatalf("no nodes: %+v", r)
	}
	if !r.PutOK || !r.GetOK {
		t.Errorf("put/get failed: %+v", r)
	}
	t.Logf("%+v", r)
}
