package natprobe

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
	"github.com/NotrinoResearch/lanfolk/internal/lanprobe"
	"github.com/NotrinoResearch/lanfolk/internal/stun"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// scrubber replaces secrets (the relay host and its addresses) in text that
// ends up in the output.
type scrubber struct{ secrets []string }

func (s *scrubber) add(v string) {
	if v != "" {
		s.secrets = append(s.secrets, v)
	}
}

func (s *scrubber) text(v string) string {
	for _, sec := range s.secrets {
		v = strings.ReplaceAll(v, sec, "relay")
	}
	return v
}

func (s *scrubber) err(err error) string {
	if err == nil {
		return ""
	}
	return s.text(err.Error())
}

// Target is a STUN destination.
type Target struct {
	Label string // shown in the output ("relay" or the STUN server name)
	Addr  netip.AddrPort
}

func randNonce() uint64 {
	var b [8]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint64(b[:])
}

func localPort(c *net.UDPConn) uint16 {
	return c.LocalAddr().(*net.UDPAddr).AddrPort().Port()
}

// stunQuery sends a Binding request from conn to dst and returns the mapped
// address.
func stunQuery(ctx context.Context, conn *net.UDPConn, dst netip.AddrPort, perTry time.Duration, tries int) (netip.AddrPort, time.Duration, error) {
	tx := stun.NewTxID()
	req := stun.BindingRequest(tx)
	buf := make([]byte, 1500)
	var lastErr error = errors.New("no answer")
	for try := 0; try < tries && ctx.Err() == nil; try++ {
		start := clock.Nanotime()
		if _, err := conn.WriteToUDPAddrPort(req, dst); err != nil {
			return netip.AddrPort{}, 0, err
		}
		conn.SetReadDeadline(time.Now().Add(perTry))
		for {
			n, src, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				lastErr = errors.New("timeout")
				break
			}
			if src.Addr().Unmap() != dst.Addr().Unmap() || src.Port() != dst.Port() {
				continue
			}
			rtx, mapped, err := stun.ParseBindingResponse(buf[:n])
			if err != nil || rtx != tx {
				continue
			}
			return netip.AddrPortFrom(mapped.Addr().Unmap(), mapped.Port()), clock.Since(start), nil
		}
	}
	if ctx.Err() != nil {
		return netip.AddrPort{}, 0, ctx.Err()
	}
	return netip.AddrPort{}, 0, lastErr
}

// QueryTargets asks every target, in order, from the one socket conn.
func QueryTargets(ctx context.Context, conn *net.UDPConn, targets []Target, sc *scrubber) []Obs {
	out := make([]Obs, 0, len(targets))
	for _, t := range targets {
		o := Obs{Label: t.Label, Dst: t.Addr}
		mapped, rtt, err := stunQuery(ctx, conn, t.Addr, 800*time.Millisecond, 3)
		if err != nil {
			o.Err = sc.err(err)
		} else {
			o.Mapped = mapped
			o.MappedStr = mapped.String()
			o.RTTMs = ms(rtt)
		}
		out = append(out, o)
	}
	return out
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func mappedPorts(obs []Obs) []uint16 {
	var p []uint16
	for _, o := range obs {
		if o.Mapped.IsValid() {
			p = append(p, o.Mapped.Port())
		}
	}
	return p
}

// resolveV4 resolves host to its first IPv4 address.
func resolveV4(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, errors.New("no IPv4 address")
	}
	return ips[0].Unmap(), nil
}

// splitHostPort splits "host:port" and applies defPort when there is no port.
func splitHostPort(s string, defPort int) (string, int, error) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		var port int
		if _, err := fmt.Sscanf(p, "%d", &port); err != nil || port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("bad port in %q", s)
		}
		return h, port, nil
	}
	return s, defPort, nil
}

// ResolveSTUN resolves the public STUN servers, keeping one entry per
// distinct IPv4 address.
func ResolveSTUN(ctx context.Context, servers []string) (targets []Target, problems []string) {
	seen := map[netip.Addr]bool{}
	for _, s := range servers {
		host, port, err := splitHostPort(s, 3478)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		ip, err := resolveV4(ctx, host)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s, err))
			continue
		}
		if seen[ip] {
			problems = append(problems, fmt.Sprintf("%s: same IP as an earlier server, skipped", s))
			continue
		}
		seen[ip] = true
		targets = append(targets, Target{Label: s, Addr: netip.AddrPortFrom(ip, uint16(port))})
	}
	return
}

// RTTStats summarises a series of round trips in milliseconds.
type RTTStats struct {
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	MinMs    float64 `json:"min_ms"`
	MedianMs float64 `json:"median_ms"`
	P95Ms    float64 `json:"p95_ms"`
	LossPct  float64 `json:"loss_pct"`
}

func rttStats(rtts []time.Duration, sent int) *RTTStats {
	s := lanprobe.Summarize(rtts, sent)
	return &RTTStats{Sent: s.Sent, Received: s.Received, MinMs: ms(s.Min), MedianMs: ms(s.P50), P95Ms: ms(s.P95), LossPct: s.LossPct}
}

// RelayReport is the relay reachability result.
type RelayReport struct {
	Configured bool      `json:"configured"`
	UDP        *RTTStats `json:"udp,omitempty"`
	UDPErr     string    `json:"udp_error,omitempty"`
	TCP        *RTTStats `json:"tcp,omitempty"`
	TCPConnect float64   `json:"tcp_connect_ms,omitempty"`
	TCPErr     string    `json:"tcp_error,omitempty"`
}

const pingSamples = 20

// PingUDP measures the UDP Ping/Pong round trip to the relay.
func PingUDP(ctx context.Context, relay netip.AddrPort, sc *scrubber) (*RTTStats, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var rtts []time.Duration
	buf := make([]byte, 1500)
	sent, lost := 0, 0
	for i := 0; i < pingSamples && ctx.Err() == nil; i++ {
		nonce := randNonce()
		start := clock.Nanotime()
		if _, err := conn.WriteToUDPAddrPort(wire.AppendPing(nil, nonce), relay); err != nil {
			return nil, errors.New(sc.err(err))
		}
		sent++
		conn.SetReadDeadline(time.Now().Add(time.Second))
		got := false
		for {
			n, src, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				break
			}
			if src.Addr().Unmap() != relay.Addr().Unmap() {
				continue
			}
			if m, err := wire.ParseRelayControl(buf[:n]); err == nil && m.Type == wire.RelayPong && m.Nonce == nonce {
				rtts = append(rtts, clock.Since(start))
				got = true
				break
			}
		}
		if !got {
			lost++
			if lost >= 5 && len(rtts) == 0 {
				break // the relay is not answering; do not wait for all samples
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(40 * time.Millisecond):
		}
	}
	if len(rtts) == 0 {
		return rttStats(nil, sent), errors.New("no Pong received (UDP blocked, wrong port or relay down)")
	}
	return rttStats(rtts, sent), nil
}

// PingTCP connects to the relay's TCP fallback and measures framed Ping/Pong.
func PingTCP(ctx context.Context, relay netip.AddrPort, sc *scrubber) (rs *RTTStats, connectMs float64, err error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	start := clock.Nanotime()
	c, err := d.DialContext(ctx, "tcp4", relay.String())
	if err != nil {
		return nil, 0, errors.New(sc.err(err))
	}
	defer c.Close()
	connectMs = ms(clock.Since(start))
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	r := bufio.NewReader(c)
	buf := make([]byte, wire.MaxStreamFrame)
	var rtts []time.Duration
	sent := 0
	for i := 0; i < pingSamples && ctx.Err() == nil; i++ {
		nonce := randNonce()
		frame := wire.AppendStreamFrame(nil, wire.AppendPing(nil, nonce))
		t0 := clock.Nanotime()
		c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
		if _, err := c.Write(frame); err != nil {
			break
		}
		sent++
		f, err := wire.ReadStreamFrame(r, buf)
		if err != nil {
			break
		}
		if m, err := wire.ParseRelayControl(f); err == nil && m.Type == wire.RelayPong && m.Nonce == nonce {
			rtts = append(rtts, clock.Since(t0))
		}
		select {
		case <-ctx.Done():
		case <-time.After(40 * time.Millisecond):
		}
	}
	if len(rtts) == 0 {
		return rttStats(nil, sent), connectMs, errors.New("connected, but no framed Pong received")
	}
	return rttStats(rtts, sent), connectMs, nil
}

// MeasureTimeouts opens one socket per idle time and asks the relay to reply
// to each after that many seconds. A reply that arrives means the NAT kept
// the mapping (and its filtering state) for at least that long with no
// traffic in between. All sockets run in parallel, so the total time is the
// largest value plus a small margin.
func MeasureTimeouts(ctx context.Context, relay netip.AddrPort, seconds []int, sc *scrubber) []TimeoutResult {
	results := make([]TimeoutResult, len(seconds))
	var wg sync.WaitGroup
	for i, sec := range seconds {
		results[i].Seconds = sec
		if sec < 0 || sec > 255 {
			results[i].Err = "idle time must be 0-255 s"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = measureOneTimeout(ctx, relay, sec, sc)
		}()
	}
	wg.Wait()
	return results
}

func measureOneTimeout(ctx context.Context, relay netip.AddrPort, sec int, sc *scrubber) TimeoutResult {
	res := TimeoutResult{Seconds: sec}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer conn.Close()
	nonce := randNonce()
	req := wire.AppendEchoRequest(nil, nonce, byte(sec))
	start := clock.Nanotime()
	// Three copies in a burst make the request robust against one lost
	// datagram; the burst is far shorter than the idle time, so it does not
	// refresh the mapping in a way that matters.
	for i := 0; i < 3; i++ {
		if _, err := conn.WriteToUDPAddrPort(req, relay); err != nil {
			res.Err = sc.err(err)
			return res
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline := time.Now().Add(time.Duration(sec)*time.Second + 4*time.Second)
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	// Cancel the blocking read when ctx is done.
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()) })
	defer stop()
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				res.Err = "interrupted"
			}
			return res // survived stays false: no reply
		}
		if src.Addr().Unmap() != relay.Addr().Unmap() {
			continue
		}
		m, err := wire.ParseRelayControl(buf[:n])
		if err != nil {
			continue
		}
		switch {
		case m.Type == wire.RelayReject:
			res.Err = fmt.Sprintf("relay rejected the echo request (code %d)", m.Code)
			return res
		case m.Type == wire.RelayEchoReply && m.Nonce == nonce:
			el := clock.Since(start)
			res.ArrivedMs = ms(el)
			// A reply that came much earlier than requested was not delayed
			// by the relay (it capped the delay); do not count it as proof.
			res.Survived = el >= time.Duration(sec)*time.Second-300*time.Millisecond
			if !res.Survived {
				res.Err = fmt.Sprintf("reply came after %.1f s, earlier than requested (relay delay cap?)", el.Seconds())
			}
			return res
		}
	}
}

// IPv6Report is the IPv6 result.
type IPv6Report struct {
	GlobalAddrs int     `json:"global_addresses"`
	STUNOK      bool    `json:"stun_ok"`
	Mapped      string  `json:"mapped,omitempty"`
	RTTMs       float64 `json:"rtt_ms,omitempty"`
	Err         string  `json:"error,omitempty"`
}

// GlobalIPv6 returns the global unicast IPv6 addresses (not ULA, not
// link-local) on the local interfaces.
func GlobalIPv6(addrs []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if a.Is6() && a.IsGlobalUnicast() && !a.IsPrivate() {
			out = append(out, a)
		}
	}
	return out
}

// LocalAddrs lists the IP addresses of all local interfaces.
func LocalAddrs() []netip.Addr {
	var out []netip.Addr
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr())
		}
	}
	return out
}

// MeasureIPv6 checks for a global IPv6 address and tries STUN over IPv6.
func MeasureIPv6(ctx context.Context, local []netip.Addr, server string) IPv6Report {
	var r IPv6Report
	r.GlobalAddrs = len(GlobalIPv6(local))
	if r.GlobalAddrs == 0 {
		r.Err = "no global IPv6 address on any interface"
		return r
	}
	host, port, err := splitHostPort(server, 19302)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip6", host)
	if err != nil || len(ips) == 0 {
		r.Err = "cannot resolve an IPv6 address for the STUN server"
		return r
	}
	conn, err := net.ListenUDP("udp6", nil)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer conn.Close()
	mapped, rtt, err := stunQuery(ctx, conn, netip.AddrPortFrom(ips[0], uint16(port)), time.Second, 3)
	if err != nil {
		r.Err = "IPv6 STUN: " + err.Error()
		return r
	}
	r.STUNOK, r.Mapped, r.RTTMs = true, mapped.String(), ms(rtt)
	return r
}
