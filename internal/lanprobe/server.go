package lanprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// ServerConfig configures the probe server.
type ServerConfig struct {
	// Bind is the address for the TCP control, UDP unicast and latency
	// sockets. Default 0.0.0.0. The broadcast and multicast sockets always
	// bind 0.0.0.0, because that is how a game receives broadcasts.
	Bind string
	// IfaceIP, when set, restricts the multicast join to the interface that
	// owns this IPv4 address. Default is every multicast-capable interface.
	IfaceIP string
	// Group is the multicast group. Default DefaultGroup.
	Group string
	// Ports to listen on. Zero values select the defaults; use a negative
	// value for "any free port" (tests).
	Ports Ports
	// Logf receives informational messages. May be nil.
	Logf func(format string, args ...any)
}

// Server is the probe server run by "lanprobe serve".
type Server struct {
	cfg    ServerConfig
	logf   func(string, ...any)
	tcp    net.Listener
	uni    *net.UDPConn
	bcast  *net.UDPConn
	mcast  *net.UDPConn
	lat    *net.UDPConn
	store  *probeStore
	joined []string

	closeOnce sync.Once
	wg        sync.WaitGroup
}

func portOrDefault(p, def int) string {
	switch {
	case p < 0:
		return "0"
	case p == 0:
		return fmt.Sprint(def)
	}
	return fmt.Sprint(p)
}

// NewServer binds every socket. Call Serve to process traffic.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Bind == "" {
		cfg.Bind = "0.0.0.0"
	}
	if cfg.Group == "" {
		cfg.Group = DefaultGroup
	}
	group, err := netip.ParseAddr(cfg.Group)
	if err != nil || !group.Is4() || !group.IsMulticast() {
		return nil, fmt.Errorf("invalid multicast group %q", cfg.Group)
	}
	s := &Server{cfg: cfg, logf: cfg.Logf, store: newProbeStore()}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	d := DefaultPorts
	fail := func(what string, err error) (*Server, error) {
		s.closeSockets()
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if s.tcp, err = net.Listen("tcp4", net.JoinHostPort(cfg.Bind, portOrDefault(cfg.Ports.TCP, d.TCP))); err != nil {
		return fail("listen tcp", err)
	}
	if s.uni, err = listenUDP4(net.JoinHostPort(cfg.Bind, portOrDefault(cfg.Ports.Unicast, d.Unicast))); err != nil {
		return fail("listen udp unicast", err)
	}
	if s.bcast, err = listenUDP4(net.JoinHostPort("0.0.0.0", portOrDefault(cfg.Ports.Broadcast, d.Broadcast)), setBroadcast); err != nil {
		return fail("listen udp broadcast", err)
	}
	// SO_REUSEADDR lets several discovery listeners share the multicast port,
	// as games do.
	if s.mcast, err = listenUDP4(net.JoinHostPort("0.0.0.0", portOrDefault(cfg.Ports.Multicast, d.Multicast)), setReuseAddr); err != nil {
		return fail("listen udp multicast", err)
	}
	if s.lat, err = listenUDP4(net.JoinHostPort(cfg.Bind, portOrDefault(cfg.Ports.Latency, d.Latency))); err != nil {
		return fail("listen udp latency", err)
	}
	s.lat.SetReadBuffer(1 << 20)
	s.lat.SetWriteBuffer(1 << 20)
	if err := s.joinGroups(group); err != nil {
		return fail("join multicast", err)
	}
	return s, nil
}

// joinGroups joins the group on the configured or all interfaces.
func (s *Server) joinGroups(group netip.Addr) error {
	var ifaceIPs []netip.Addr
	if s.cfg.IfaceIP != "" {
		ip, err := netip.ParseAddr(s.cfg.IfaceIP)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("invalid -iface-ip %q", s.cfg.IfaceIP)
		}
		ifaceIPs = append(ifaceIPs, ip)
	} else {
		ifaces, _ := net.Interfaces()
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
				continue
			}
			addrs, _ := ifi.Addrs()
			for _, a := range addrs {
				if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() {
					ifaceIPs = append(ifaceIPs, p.Addr())
				}
			}
		}
	}
	var lastErr error
	for _, ip := range ifaceIPs {
		if err := joinOn(s.mcast, group, ip); err != nil {
			lastErr = err
			s.logf("multicast join on %v failed: %v", ip, err)
			continue
		}
		s.joined = append(s.joined, ip.String())
	}
	if s.cfg.IfaceIP != "" && len(s.joined) == 0 {
		return fmt.Errorf("cannot join %v on %s: %v", group, s.cfg.IfaceIP, lastErr)
	}
	if len(s.joined) == 0 {
		s.logf("warning: joined multicast group %v on no interface", group)
	}
	return nil
}

// Ports returns the ports actually bound.
func (s *Server) Ports() Ports {
	return Ports{
		TCP:       s.tcp.Addr().(*net.TCPAddr).Port,
		Unicast:   s.uni.LocalAddr().(*net.UDPAddr).Port,
		Broadcast: s.bcast.LocalAddr().(*net.UDPAddr).Port,
		Multicast: s.mcast.LocalAddr().(*net.UDPAddr).Port,
		Latency:   s.lat.LocalAddr().(*net.UDPAddr).Port,
	}
}

// JoinedInterfaces lists the interface addresses that joined the group.
func (s *Server) JoinedInterfaces() []string { return append([]string(nil), s.joined...) }

func (s *Server) closeSockets() {
	if s.tcp != nil {
		s.tcp.Close()
	}
	// Close on a nil *net.UDPConn is a harmless no-op, so a partially
	// constructed server can be closed too.
	for _, c := range []*net.UDPConn{s.uni, s.bcast, s.mcast, s.lat} {
		c.Close()
	}
}

// Close stops the server.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.closeSockets()
		s.wg.Wait()
	})
}

// Serve processes traffic until ctx is cancelled, then closes the server.
func (s *Server) Serve(ctx context.Context) error {
	go func() { <-ctx.Done(); s.Close() }()
	s.wg.Add(5)
	go func() { defer s.wg.Done(); s.acceptLoop() }()
	go func() { defer s.wg.Done(); s.probeLoop(s.uni, true) }()
	go func() { defer s.wg.Done(); s.probeLoop(s.bcast, false) }()
	go func() { defer s.wg.Done(); s.probeLoop(s.mcast, false) }()
	go func() { defer s.wg.Done(); s.echoLoop(s.lat) }()
	<-ctx.Done()
	s.Close()
	return nil
}

func (s *Server) acceptLoop() {
	for {
		c, err := s.tcp.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer c.Close()
	remote := ""
	if ta, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		remote = ta.IP.String()
	}
	for {
		c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		m, err := ReadMsg(c)
		if err != nil {
			return
		}
		var reply Msg
		switch m.Type {
		case MsgHello:
			reply = Msg{Type: MsgHelloOK, Token: m.Token, Remote: remote}
		case MsgData:
			sum := sha256.Sum256(m.Data)
			reply = Msg{Type: MsgDataOK, Token: m.Token, Sum: hex.EncodeToString(sum[:]), Remote: remote}
		case MsgReport:
			reply = Msg{Type: MsgReportR, Token: m.Token, Probes: s.store.report(m.Token)}
		default:
			reply = Msg{Type: MsgError, Err: "unknown message type"}
		}
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if WriteMsg(c, &reply) != nil {
			return
		}
	}
}

// probeLoop records every probe datagram received on conn and, when echo is
// set, sends the datagram back to its source.
func (s *Server) probeLoop(conn *net.UDPConn, echo bool) {
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p, err := DecodeProbe(buf[:n])
		if err != nil {
			continue // stray traffic
		}
		s.store.add(p, src.Addr().Unmap().String())
		if echo {
			conn.WriteToUDPAddrPort(buf[:n], src)
		}
	}
}

// echoLoop returns every datagram to its sender unchanged.
func (s *Server) echoLoop(conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		conn.WriteToUDPAddrPort(buf[:n], src)
	}
}

// probeStore keeps received probes by token.
type probeStore struct {
	mu sync.Mutex
	m  map[string]*tokenProbes
}

type tokenProbes struct {
	last   time.Time
	probes map[string]*ProbeRecord
}

const (
	storeTTL     = 10 * time.Minute
	storeMaxKeys = 1000
)

func newProbeStore() *probeStore { return &probeStore{m: make(map[string]*tokenProbes)} }

func (st *probeStore) add(p Probe, src string) {
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.m) >= storeMaxKeys {
		for k, v := range st.m {
			if now.Sub(v.last) > storeTTL {
				delete(st.m, k)
			}
		}
		if len(st.m) >= storeMaxKeys {
			return
		}
	}
	t := st.m[p.Token]
	if t == nil {
		t = &tokenProbes{probes: make(map[string]*ProbeRecord)}
		st.m[p.Token] = t
	}
	t.last = now
	r := t.probes[p.Name]
	if r == nil {
		r = &ProbeRecord{Name: p.Name, Src: src, Claimed: p.Claimed, Match: true}
		t.probes[p.Name] = r
	}
	r.Count++
	if src != p.Claimed {
		if r.Match {
			r.BadSrc = src
		}
		r.Match = false
	}
}

func (st *probeStore) report(token string) []ProbeRecord {
	st.mu.Lock()
	defer st.mu.Unlock()
	t := st.m[token]
	if t == nil {
		return nil
	}
	out := make([]ProbeRecord, 0, len(t.probes))
	for _, r := range t.probes {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
