package engine

import (
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
	"github.com/NotrinoResearch/lanfolk/internal/tunwrap"
)

// TestTunnelOverhead measures the software overhead of the tunnel on one
// machine: UDP echo round trips through two engines (WireGuard, bind, LAN
// layer, in-memory adapters) against raw UDP echo on loopback. It excludes
// the Wintun driver and the network. Run with LANFOLK_OVERHEAD=1, optionally
// LANFOLK_OVERHEAD_COUNT (default 10000, the plan's sample size).
func TestTunnelOverhead(t *testing.T) {
	if os.Getenv("LANFOLK_OVERHEAD") == "" {
		t.Skip("set LANFOLK_OVERHEAD=1 to measure tunnel overhead")
	}
	count := 10000
	if v, err := strconv.Atoi(os.Getenv("LANFOLK_OVERHEAD_COUNT")); err == nil && v > 0 {
		count = v
	}
	const size, interval = 200, time.Millisecond
	raw := rawEcho(t, count, size, interval)

	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	sendUntil(t, a, b, 7, []byte("warm up"))
	waitFor(t, 10*time.Second, "direct path", func() bool { return a.peer(b).Path == "direct" })
	tun := tunnelEcho(t, a, b, count, size, interval)

	rp50, rp99 := pct(raw, 50), pct(raw, 99)
	tp50, tp99 := pct(tun, 50), pct(tun, 99)
	t.Logf("raw UDP loopback RTT:  p50 %v  p99 %v  (%d samples)", rp50, rp99, len(raw))
	t.Logf("tunnel RTT:            p50 %v  p99 %v  (%d samples, %d lost)", tp50, tp99, len(tun), count-len(tun))
	t.Logf("overhead:              p50 %v  p99 %v  (target <= 1 ms p50, <= 3 ms p99)", tp50-rp50, tp99-rp99)
	if tp50-rp50 > time.Millisecond || tp99-rp99 > 3*time.Millisecond {
		t.Errorf("tunnel overhead above the plan target")
	}
	if loss := float64(count-len(tun)) / float64(count); loss > 0.001 {
		t.Errorf("tunnel lost %.3f%% of packets (target below 0.1%%)", loss*100)
	}
}

func pct(d []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 0 {
		return 0
	}
	return s[(len(s)-1)*p/100]
}

func rawEcho(t *testing.T, count, size int, interval time.Duration) []time.Duration {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := srv.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			srv.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dst := srv.LocalAddr().(*net.UDPAddr).AddrPort()
	var out []time.Duration
	buf := make([]byte, 2048)
	pkt := make([]byte, size)
	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint64(pkt, uint64(clock.Nanotime()))
		c.WriteToUDPAddrPort(pkt, dst)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err := c.ReadFromUDPAddrPort(buf); err == nil {
			out = append(out, clock.Since(int64(binary.BigEndian.Uint64(buf))))
		}
		time.Sleep(interval)
	}
	return out
}

func tunnelEcho(t *testing.T, a, b *node, count, size int, interval time.Duration) []time.Duration {
	stop := make(chan struct{})
	defer close(stop)
	// Mehmet's "application" echoes every packet back.
	go func() {
		for {
			select {
			case <-stop:
				return
			case pkt := <-b.memtun().ToOS():
				src, dst, payload, ok := tunwrap.ParseUDP4(pkt)
				if ok && dst.Port() == 7 {
					b.memtun().InjectFromOS(tunwrap.BuildUDP4(dst, src, 64, payload))
				}
			}
		}
	}()
	dst := netip.AddrPortFrom(b.vip(), 7)
	src := netip.AddrPortFrom(a.vip(), 40007)
	var out []time.Duration
	payload := make([]byte, size)
	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint64(payload, uint64(clock.Nanotime()))
		binary.BigEndian.PutUint64(payload[8:], uint64(i))
		a.memtun().InjectFromOS(tunwrap.BuildUDP4(src, dst, 64, payload))
		deadline := time.After(time.Second)
	wait:
		for {
			select {
			case pkt := <-a.memtun().ToOS():
				_, _, p, ok := tunwrap.ParseUDP4(pkt)
				if ok && len(p) >= 16 && binary.BigEndian.Uint64(p[8:]) == uint64(i) {
					out = append(out, clock.Since(int64(binary.BigEndian.Uint64(p))))
					break wait
				}
			case <-deadline:
				break wait
			}
		}
		time.Sleep(interval)
	}
	return out
}
