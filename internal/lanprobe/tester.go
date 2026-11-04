package lanprobe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
)

// Status is the outcome of one behavior test.
type Status string

// Test outcomes.
const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Test IDs, usable in TestConfig.Skip.
const (
	IDTCP           = "tcp-unicast"
	IDUDP           = "udp-unicast"
	IDSubnetBcast   = "subnet-broadcast"
	IDGlobalBound   = "global-broadcast-bound"
	IDGlobalAny     = "global-broadcast-any"
	IDMcastIF       = "multicast-if"
	IDMcastDefault  = "multicast-default"
	IDIPInPayload   = "ip-in-payload"
	IDPrivateRange  = "private-range"
	IDBlockedPorts  = "blocked-ports"
	udpProbeRepeats = 3
)

// DefaultBlockedPorts are the Windows service ports that must not be
// reachable through the Lanfolk adapter.
var DefaultBlockedPorts = []int{135, 139, 445, 3389, 5985, 5986}

// Result is the outcome of one behavior test.
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	// Core results decide the exit code. The non-core variants (sockets bound
	// to 0.0.0.0, default multicast interface) depend on the interface
	// metric and are expected to fail without compatibility mode.
	Core bool `json:"core"`
}

// TestConfig configures "lanprobe test".
type TestConfig struct {
	Peer  string // peer IPv4 address (required)
	Local string // local virtual IPv4; detected from the route to Peer if empty
	Ports Ports  // zero values select the defaults
	Group string // multicast group, default DefaultGroup
	// SubnetBroadcast is the directed broadcast address, default 10.77.0.255.
	SubnetBroadcast string
	// GlobalBroadcast is the limited broadcast address, default 255.255.255.255.
	GlobalBroadcast string
	// BlockedPorts to try, default DefaultBlockedPorts.
	BlockedPorts []int
	// Skip lists test IDs to skip.
	Skip map[string]bool
	// DialTimeout bounds TCP connects. Default 3 s (2 s for blocked ports).
	DialTimeout time.Duration
	Logf        func(format string, args ...any)
}

func (c *TestConfig) defaults() {
	d := DefaultPorts
	if c.Ports.TCP == 0 {
		c.Ports.TCP = d.TCP
	}
	if c.Ports.Unicast == 0 {
		c.Ports.Unicast = d.Unicast
	}
	if c.Ports.Broadcast == 0 {
		c.Ports.Broadcast = d.Broadcast
	}
	if c.Ports.Multicast == 0 {
		c.Ports.Multicast = d.Multicast
	}
	if c.Ports.Latency == 0 {
		c.Ports.Latency = d.Latency
	}
	if c.Group == "" {
		c.Group = DefaultGroup
	}
	if c.SubnetBroadcast == "" {
		c.SubnetBroadcast = "10.77.0.255"
	}
	if c.GlobalBroadcast == "" {
		c.GlobalBroadcast = "255.255.255.255"
	}
	if c.BlockedPorts == nil {
		c.BlockedPorts = DefaultBlockedPorts
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 3 * time.Second
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// udpCase describes one datagram test sent to the server's broadcast or
// multicast socket.
type udpCase struct {
	id      string
	title   string
	core    bool
	dst     netip.AddrPort
	bindIP  netip.Addr // invalid: 0.0.0.0
	mcastIF bool       // set IP_MULTICAST_IF to the local IP
	hint    string     // appended to a FAIL
}

// RunTest runs every behavior test against the peer and returns one result
// per behavior, in display order.
func RunTest(ctx context.Context, cfg TestConfig) ([]Result, error) {
	cfg.defaults()
	peer, err := netip.ParseAddr(cfg.Peer)
	if err != nil || !peer.Is4() {
		return nil, fmt.Errorf("-peer must be an IPv4 address, got %q", cfg.Peer)
	}
	local, err := resolveLocal(cfg.Local, peer, cfg.Ports.Unicast)
	if err != nil {
		return nil, err
	}
	subnet, err := netip.ParseAddr(cfg.SubnetBroadcast)
	if err != nil || !subnet.Is4() {
		return nil, fmt.Errorf("invalid subnet broadcast address %q", cfg.SubnetBroadcast)
	}
	global, err := netip.ParseAddr(cfg.GlobalBroadcast)
	if err != nil || !global.Is4() {
		return nil, fmt.Errorf("invalid global broadcast address %q", cfg.GlobalBroadcast)
	}
	group, err := netip.ParseAddr(cfg.Group)
	if err != nil || !group.Is4() || !group.IsMulticast() {
		return nil, fmt.Errorf("invalid multicast group %q", cfg.Group)
	}
	token := NewToken()
	claimed := local.String()
	cfg.Logf("peer %v, local %v, token %s", peer, local, token)

	cases := []udpCase{
		{id: IDSubnetBcast, title: "Subnet broadcast", core: true,
			dst: netip.AddrPortFrom(subnet, uint16(cfg.Ports.Broadcast))},
		{id: IDGlobalBound, title: "Global broadcast (socket bound to local IP)", core: true,
			dst: netip.AddrPortFrom(global, uint16(cfg.Ports.Broadcast)), bindIP: local},
		{id: IDGlobalAny, title: "Global broadcast (socket bound to 0.0.0.0)",
			dst:  netip.AddrPortFrom(global, uint16(cfg.Ports.Broadcast)),
			hint: "expected unless the adapter metric is lowest (compatibility mode)"},
		{id: IDMcastIF, title: "Multicast (IP_MULTICAST_IF = local IP)", core: true,
			dst: netip.AddrPortFrom(group, uint16(cfg.Ports.Multicast)), bindIP: local, mcastIF: true},
		{id: IDMcastDefault, title: "Multicast (default interface)",
			dst:  netip.AddrPortFrom(group, uint16(cfg.Ports.Multicast)),
			hint: "expected unless the adapter metric is lowest (compatibility mode)"},
	}

	byID := map[string]Result{}
	set := func(r Result) { byID[r.ID] = r }

	// Control connection: TCP unicast and the result channel.
	ctl, ctlErr := net.DialTimeout("tcp4", net.JoinHostPort(peer.String(), fmt.Sprint(cfg.Ports.TCP)), cfg.DialTimeout)
	if ctlErr != nil {
		set(Result{ID: IDTCP, Title: "TCP unicast", Status: Fail, Core: true,
			Detail: fmt.Sprintf("control connection to %v:%d failed: %v (is `lanprobe serve` running and the firewall open?)", peer, cfg.Ports.TCP, ctlErr)})
	} else {
		defer ctl.Close()
		if cfg.Skip[IDTCP] {
			set(skipped(IDTCP, "TCP unicast", true))
		} else {
			set(testTCP(ctl, token, local))
		}
	}

	// UDP unicast through the echo port.
	if cfg.Skip[IDUDP] {
		set(skipped(IDUDP, "UDP unicast", true))
	} else {
		set(testUDPUnicast(ctx, peer, cfg.Ports.Unicast, token, claimed))
	}

	// Broadcast and multicast probes, reported back over the control channel.
	sentAny := false
	for _, c := range cases {
		if cfg.Skip[c.id] {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := sendProbeCase(c, token, claimed); err != nil {
			set(Result{ID: c.id, Title: c.title, Core: c.core, Status: Fail, Detail: "send failed: " + err.Error()})
			continue
		}
		sentAny = true
	}
	var recs map[string]ProbeRecord
	var repErr error
	if ctlErr != nil {
		repErr = errors.New("no control connection, so reception cannot be confirmed")
	} else {
		if sentAny {
			sleepCtx(ctx, 500*time.Millisecond)
		}
		recs, repErr = fetchReport(ctl, token)
	}
	for _, c := range cases {
		if _, done := byID[c.id]; done {
			continue
		}
		if cfg.Skip[c.id] {
			set(skipped(c.id, c.title, c.core))
			continue
		}
		if repErr != nil {
			set(Result{ID: c.id, Title: c.title, Core: c.core, Status: Fail, Detail: repErr.Error()})
			continue
		}
		set(caseResult(c, recs[c.id]))
	}

	if cfg.Skip[IDIPInPayload] {
		set(skipped(IDIPInPayload, "IP in payload equals packet source", true))
	} else if repErr != nil {
		set(Result{ID: IDIPInPayload, Title: "IP in payload equals packet source", Core: true, Status: Fail, Detail: repErr.Error()})
	} else {
		set(ipInPayloadResult(recs))
	}

	if cfg.Skip[IDPrivateRange] {
		set(skipped(IDPrivateRange, "Peer IP is a private (RFC 1918) address", true))
	} else {
		set(privateRangeResult(peer))
	}

	if cfg.Skip[IDBlockedPorts] {
		set(skipped(IDBlockedPorts, "Windows service ports blocked", true))
	} else {
		set(testBlockedPorts(ctx, peer, cfg.BlockedPorts, cfg.DialTimeout))
	}

	order := []string{IDTCP, IDUDP, IDSubnetBcast, IDGlobalBound, IDGlobalAny, IDMcastIF, IDMcastDefault, IDIPInPayload, IDPrivateRange, IDBlockedPorts}
	out := make([]Result, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func skipped(id, title string, core bool) Result {
	return Result{ID: id, Title: title, Status: Skip, Core: core, Detail: "skipped"}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// resolveLocal returns the local IPv4 to claim in payloads: the explicit
// value, or the source address the OS picks for a route to the peer.
func resolveLocal(local string, peer netip.Addr, port int) (netip.Addr, error) {
	if local != "" {
		ip, err := netip.ParseAddr(local)
		if err != nil || !ip.Is4() {
			return netip.Addr{}, fmt.Errorf("-local must be an IPv4 address, got %q", local)
		}
		return ip, nil
	}
	c, err := net.Dial("udp4", net.JoinHostPort(peer.String(), fmt.Sprint(port)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("cannot find a route to %v: %w", peer, err)
	}
	defer c.Close()
	ap := c.LocalAddr().(*net.UDPAddr).AddrPort()
	return ap.Addr().Unmap(), nil
}

func testTCP(ctl net.Conn, token string, local netip.Addr) Result {
	r := Result{ID: IDTCP, Title: "TCP unicast", Core: true}
	block := make([]byte, 32<<10)
	rand.Read(block)
	want := sha256.Sum256(block)
	start := clock.Nanotime()
	ctl.SetDeadline(time.Now().Add(5 * time.Second))
	defer ctl.SetDeadline(time.Time{})
	if err := WriteMsg(ctl, &Msg{Type: MsgHello, Token: token}); err != nil {
		r.Status, r.Detail = Fail, "hello: "+err.Error()
		return r
	}
	ack, err := ReadMsg(ctl)
	if err != nil || ack.Type != MsgHelloOK {
		r.Status, r.Detail = Fail, fmt.Sprintf("hello reply: %v", firstErr(err, ack))
		return r
	}
	if err := WriteMsg(ctl, &Msg{Type: MsgData, Token: token, Data: block}); err != nil {
		r.Status, r.Detail = Fail, "data: "+err.Error()
		return r
	}
	da, err := ReadMsg(ctl)
	if err != nil || da.Type != MsgDataOK {
		r.Status, r.Detail = Fail, fmt.Sprintf("data reply: %v", firstErr(err, da))
		return r
	}
	if da.Sum != hex.EncodeToString(want[:]) {
		r.Status, r.Detail = Fail, "32 KiB block was corrupted in transit"
		return r
	}
	r.Status = Pass
	r.Detail = fmt.Sprintf("32 KiB echoed intact in %s; server saw source %s", Ms(clock.Since(start)), ack.Remote)
	if ack.Remote != "" && ack.Remote != local.String() {
		r.Detail += fmt.Sprintf(" (differs from local %v)", local)
	}
	return r
}

func firstErr(err error, m *Msg) string {
	if err != nil {
		return err.Error()
	}
	if m != nil {
		if m.Err != "" {
			return m.Err
		}
		return "unexpected message " + m.Type
	}
	return "unknown error"
}

func testUDPUnicast(ctx context.Context, peer netip.Addr, port int, token, claimed string) Result {
	r := Result{ID: IDUDP, Title: "UDP unicast", Core: true}
	conn, err := listenUDP4("0.0.0.0:0")
	if err != nil {
		r.Status, r.Detail = Fail, err.Error()
		return r
	}
	defer conn.Close()
	dst := netip.AddrPortFrom(peer, uint16(port))
	payload := Probe{Token: token, Name: IDUDP, Claimed: claimed}.Encode()
	buf := make([]byte, 2048)
	for attempt := 0; attempt < 4 && ctx.Err() == nil; attempt++ {
		start := clock.Nanotime()
		if _, err := conn.WriteToUDPAddrPort(payload, dst); err != nil {
			r.Status, r.Detail = Fail, "send failed: "+err.Error()
			return r
		}
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		for {
			n, src, err := conn.ReadFromUDPAddrPort(buf)
			if err != nil {
				break
			}
			if bytes.Equal(buf[:n], payload) && src.Addr().Unmap() == peer {
				r.Status = Pass
				r.Detail = fmt.Sprintf("echo from %v in %s (attempt %d)", src, Ms(clock.Since(start)), attempt+1)
				return r
			}
		}
	}
	r.Status = Fail
	r.Detail = fmt.Sprintf("no echo from %v:%d after 4 attempts", peer, port)
	return r
}

// sendProbeCase sends udpProbeRepeats datagrams for c.
func sendProbeCase(c udpCase, token, claimed string) error {
	bind := "0.0.0.0:0"
	if c.bindIP.IsValid() {
		bind = net.JoinHostPort(c.bindIP.String(), "0")
	}
	opts := []func(fd uintptr) error{setBroadcast}
	if c.mcastIF {
		opts = append(opts, withMulticastIF(c.bindIP))
	}
	conn, err := listenUDP4(bind, opts...)
	if err != nil {
		return err
	}
	defer conn.Close()
	var sendErr error
	ok := 0
	for i := 0; i < udpProbeRepeats; i++ {
		p := Probe{Token: token, Name: c.id, Claimed: claimed, Seq: i}.Encode()
		if _, err := conn.WriteToUDPAddrPort(p, c.dst); err != nil {
			if sendErr == nil {
				sendErr = err
			}
		} else {
			ok++
		}
		time.Sleep(40 * time.Millisecond)
	}
	if ok == 0 {
		return sendErr
	}
	return nil
}

func fetchReport(ctl net.Conn, token string) (map[string]ProbeRecord, error) {
	ctl.SetDeadline(time.Now().Add(5 * time.Second))
	defer ctl.SetDeadline(time.Time{})
	if err := WriteMsg(ctl, &Msg{Type: MsgReport, Token: token}); err != nil {
		return nil, fmt.Errorf("report request: %w", err)
	}
	m, err := ReadMsg(ctl)
	if err != nil || m.Type != MsgReportR {
		return nil, fmt.Errorf("report reply: %s", firstErr(err, m))
	}
	out := make(map[string]ProbeRecord, len(m.Probes))
	for _, p := range m.Probes {
		out[p.Name] = p
	}
	return out, nil
}

func caseResult(c udpCase, rec ProbeRecord) Result {
	r := Result{ID: c.id, Title: c.title, Core: c.core}
	if rec.Count == 0 {
		r.Status = Fail
		r.Detail = "not received"
		if c.hint != "" {
			r.Detail += " (" + c.hint + ")"
		}
		return r
	}
	r.Status = Pass
	r.Detail = fmt.Sprintf("received %d/%d, source %s", rec.Count, udpProbeRepeats, rec.Src)
	return r
}

// ipInPayloadResult checks that the IP a sender writes into its payload
// equals the packet's source address, as seen by the server.
func ipInPayloadResult(recs map[string]ProbeRecord) Result {
	r := Result{ID: IDIPInPayload, Title: "IP in payload equals packet source", Core: true}
	primary := []string{IDUDP, IDSubnetBcast, IDGlobalBound, IDMcastIF}
	var checked, bad, notes []string
	for _, id := range primary {
		rec, ok := recs[id]
		if !ok || rec.Count == 0 {
			continue
		}
		if rec.Match {
			checked = append(checked, id)
		} else {
			bad = append(bad, fmt.Sprintf("%s: payload %s, source %s", id, rec.Claimed, rec.BadSrc))
		}
	}
	for _, id := range []string{IDGlobalAny, IDMcastDefault} {
		if rec, ok := recs[id]; ok && rec.Count > 0 && !rec.Match {
			notes = append(notes, fmt.Sprintf("%s: payload %s, source %s", id, rec.Claimed, rec.BadSrc))
		}
	}
	switch {
	case len(bad) > 0:
		r.Status = Fail
		r.Detail = strings.Join(bad, "; ")
	case len(checked) == 0:
		r.Status = Fail
		r.Detail = "no probe reached the server, nothing to compare"
	default:
		r.Status = Pass
		r.Detail = "payload IP equals source IP for " + strings.Join(checked, ", ")
	}
	if len(notes) > 0 {
		r.Detail += " | note (not counted): " + strings.Join(notes, "; ")
	}
	return r
}

func privateRangeResult(peer netip.Addr) Result {
	r := Result{ID: IDPrivateRange, Title: "Peer IP is a private (RFC 1918) address", Core: true}
	if peer.Is4() && peer.IsPrivate() {
		r.Status = Pass
		r.Detail = peer.String() + " is private"
		if !netip.MustParsePrefix("10.77.0.0/24").Contains(peer) {
			r.Detail += " (outside the Lanfolk network 10.77.0.0/24)"
		}
	} else {
		r.Status = Fail
		r.Detail = peer.String() + " is not in 10/8, 172.16/12 or 192.168/16"
	}
	return r
}

// testBlockedPorts tries TCP connects to ports that must be unreachable.
func testBlockedPorts(ctx context.Context, peer netip.Addr, ports []int, timeout time.Duration) Result {
	r := Result{ID: IDBlockedPorts, Title: "Windows service ports blocked (135, 139, 445, 3389, 5985, 5986)", Core: true}
	if timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	type res struct {
		port int
		open bool
	}
	ch := make(chan res, len(ports))
	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: timeout}
			c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(peer.String(), fmt.Sprint(p)))
			if err == nil {
				c.Close()
			}
			ch <- res{p, err == nil}
		}()
	}
	wg.Wait()
	close(ch)
	var all []res
	for x := range ch {
		all = append(all, x)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].port < all[j].port })
	var parts []string
	open := 0
	for _, x := range all {
		if x.open {
			open++
			parts = append(parts, fmt.Sprintf("%d OPEN", x.port))
		} else {
			parts = append(parts, fmt.Sprintf("%d blocked", x.port))
		}
	}
	r.Detail = strings.Join(parts, ", ")
	if open == 0 {
		r.Status = Pass
	} else {
		r.Status = Fail
		r.Detail += " (these ports must not be reachable over the virtual network)"
	}
	return r
}

// Failed reports whether any core result failed.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Core && r.Status == Fail {
			return true
		}
	}
	return false
}

// WriteResults prints the results as an aligned list.
func WriteResults(w io.Writer, rs []Result) {
	width := 0
	for _, r := range rs {
		if len(r.Title) > width {
			width = len(r.Title)
		}
	}
	for _, r := range rs {
		mark := ""
		if !r.Core {
			mark = " (informational)"
		}
		fmt.Fprintf(w, "%-4s  %-*s  %s%s\n", r.Status, width, r.Title, r.Detail, mark)
	}
}

// MarkdownRow returns a header and one Markdown row of the results for
// docs/MEASUREMENTS.md.
func MarkdownRow(label string, rs []Result) (header, row string) {
	cols := []string{"Setup"}
	vals := []string{strings.ReplaceAll(label, "|", "/")}
	short := map[string]string{
		IDTCP: "TCP", IDUDP: "UDP", IDSubnetBcast: "Subnet bcast", IDGlobalBound: "Global bcast (bound)",
		IDGlobalAny: "Global bcast (0.0.0.0)", IDMcastIF: "Mcast (IF set)", IDMcastDefault: "Mcast (default)",
		IDIPInPayload: "IP in payload", IDPrivateRange: "Private range", IDBlockedPorts: "Blocked ports",
	}
	for _, r := range rs {
		cols = append(cols, short[r.ID])
		vals = append(vals, string(r.Status))
	}
	sep := make([]string, len(cols))
	for i := range sep {
		sep[i] = "---"
	}
	return "| " + strings.Join(cols, " | ") + " |\n| " + strings.Join(sep, " | ") + " |",
		"| " + strings.Join(vals, " | ") + " |"
}
