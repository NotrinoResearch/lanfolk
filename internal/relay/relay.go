// Package relay implements the shared Lanfolk relay: a STUN responder, a
// session-based packet forwarder over UDP and TCP, and a local metrics
// endpoint. The relay never decrypts payloads and stores nothing on disk.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/stun"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Config controls the relay.
type Config struct {
	PublicHost       string
	UDPAddr          string
	TCPAddr          string // empty disables the TCP fallback
	MetricsAddr      string // empty disables the metrics endpoint
	MaxSessionKbps   int
	MaxSessionsPerIP int
	MaxSessions      int
	MaxTCPConnsPerIP int
	IdleTimeout      time.Duration
	SweepInterval    time.Duration
	ControlPerIPRate float64 // control messages per second per source IP
	MaxEchoDelay     time.Duration
	Logger           *slog.Logger
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		UDPAddr:          ":3478",
		TCPAddr:          ":443",
		MetricsAddr:      "127.0.0.1:9100",
		MaxSessionKbps:   2000,
		MaxSessionsPerIP: 600,
		MaxSessions:      100000,
		MaxTCPConnsPerIP: 16,
		IdleTimeout:      120 * time.Second,
		SweepInterval:    10 * time.Second,
		ControlPerIPRate: 200,
		MaxEchoDelay:     180 * time.Second,
	}
}

type endpoint struct {
	udp netip.AddrPort
	tcp *tcpConn
}

func (e endpoint) ip() netip.Addr {
	if e.tcp != nil {
		return e.tcp.remote.Addr()
	}
	return e.udp.Addr()
}

func (e endpoint) addr() netip.AddrPort {
	if e.tcp != nil {
		return e.tcp.remote
	}
	return e.udp
}

func (e endpoint) equal(o endpoint) bool {
	if e.tcp != nil || o.tcp != nil {
		return e.tcp == o.tcp
	}
	return e.udp == o.udp
}

type party struct {
	token    wire.Token
	ep       endpoint // guarded by Server.mu
	lastSeen atomic.Int64
}

type session struct {
	id      wire.SessionID
	parties [2]*party // guarded by Server.mu
	bucket  tokenBucket
}

// Server is a running relay.
type Server struct {
	cfg      Config
	log      *slog.Logger
	udp      *net.UDPConn
	tcpLn    net.Listener
	httpLn   net.Listener
	mu       sync.RWMutex
	sessions map[wire.SessionID]*session
	perIP    map[netip.Addr]int
	tcpMu    sync.Mutex
	tcpPerIP map[netip.Addr]int
	limiter  *ipLimiter
	echoes   atomic.Int64
	metrics  metrics
	started  time.Time
}

// New binds the relay sockets. Call Serve to start processing.
func New(cfg Config) (*Server, error) {
	def := DefaultConfig()
	if cfg.MaxSessionKbps <= 0 {
		cfg.MaxSessionKbps = def.MaxSessionKbps
	}
	if cfg.MaxSessionsPerIP <= 0 {
		cfg.MaxSessionsPerIP = def.MaxSessionsPerIP
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = def.MaxSessions
	}
	if cfg.MaxTCPConnsPerIP <= 0 {
		cfg.MaxTCPConnsPerIP = def.MaxTCPConnsPerIP
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = def.IdleTimeout
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = def.SweepInterval
	}
	if cfg.ControlPerIPRate <= 0 {
		cfg.ControlPerIPRate = def.ControlPerIPRate
	}
	if cfg.MaxEchoDelay <= 0 {
		cfg.MaxEchoDelay = def.MaxEchoDelay
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg:      cfg,
		log:      cfg.Logger,
		sessions: make(map[wire.SessionID]*session),
		perIP:    make(map[netip.Addr]int),
		tcpPerIP: make(map[netip.Addr]int),
		limiter:  newIPLimiter(cfg.ControlPerIPRate, cfg.ControlPerIPRate*3),
		started:  time.Now(),
	}
	ua, err := net.ResolveUDPAddr("udp", cfg.UDPAddr)
	if err != nil {
		return nil, fmt.Errorf("udp address: %w", err)
	}
	if s.udp, err = net.ListenUDP("udp", ua); err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}
	s.udp.SetReadBuffer(4 << 20)
	s.udp.SetWriteBuffer(4 << 20)
	if cfg.TCPAddr != "" {
		if s.tcpLn, err = net.Listen("tcp", cfg.TCPAddr); err != nil {
			s.udp.Close()
			return nil, fmt.Errorf("listen tcp: %w", err)
		}
	}
	if cfg.MetricsAddr != "" {
		if s.httpLn, err = net.Listen("tcp", cfg.MetricsAddr); err != nil {
			s.udp.Close()
			if s.tcpLn != nil {
				s.tcpLn.Close()
			}
			return nil, fmt.Errorf("listen metrics: %w", err)
		}
	}
	return s, nil
}

// UDPAddr returns the bound UDP address.
func (s *Server) UDPAddr() netip.AddrPort { return s.udp.LocalAddr().(*net.UDPAddr).AddrPort() }

// TCPAddr returns the bound TCP address or nil.
func (s *Server) TCPAddr() net.Addr {
	if s.tcpLn == nil {
		return nil
	}
	return s.tcpLn.Addr()
}

// MetricsAddr returns the bound metrics address or nil.
func (s *Server) MetricsAddr() net.Addr {
	if s.httpLn == nil {
		return nil
	}
	return s.httpLn.Addr()
}

// Serve processes traffic until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.udpLoop() }()
	if s.tcpLn != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.acceptLoop() }()
	}
	stopHTTP := func() {}
	if s.httpLn != nil {
		stopHTTP = s.serveHTTP(&wg)
	}
	wg.Add(1)
	go func() { defer wg.Done(); s.sweepLoop(ctx) }()
	s.log.Info("relay started", "udp", s.UDPAddr().String(), "tcp", addrString(s.TCPAddr()),
		"metrics", addrString(s.MetricsAddr()), "max_session_kbps", s.cfg.MaxSessionKbps)
	<-ctx.Done()
	s.udp.Close()
	if s.tcpLn != nil {
		s.tcpLn.Close()
	}
	stopHTTP()
	s.closeAllTCP()
	wg.Wait()
	return nil
}

func addrString(a net.Addr) string {
	if a == nil {
		return "disabled"
	}
	return a.String()
}

func (s *Server) udpLoop() {
	buf := make([]byte, wire.MaxPacket+64)
	for {
		n, src, err := s.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.metrics.udpPackets.Add(1)
		s.metrics.udpBytes.Add(uint64(n))
		if n > wire.MaxPacket {
			s.metrics.droppedInvalid.Add(1)
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		s.handle(buf[:n], endpoint{udp: src})
	}
}

func (s *Server) send(ep endpoint, b []byte) {
	if ep.tcp != nil {
		ep.tcp.send(b)
		return
	}
	s.udp.WriteToUDPAddrPort(b, ep.udp)
}

func (s *Server) handle(b []byte, src endpoint) {
	switch wire.Classify(b) {
	case wire.ClassRelayData:
		s.handleData(b, src)
	case wire.ClassRelayControl:
		if !s.limiter.allow(src.ip()) {
			s.metrics.droppedRate.Add(1)
			return
		}
		s.handleControl(b, src)
	case wire.ClassSTUN:
		if !s.limiter.allow(src.ip()) {
			s.metrics.droppedRate.Add(1)
			return
		}
		if tx, ok := stun.ParseBindingRequest(b); ok {
			s.metrics.stunRequests.Add(1)
			s.send(src, stun.BindingResponse(tx, src.addr()))
		}
	default:
		s.metrics.droppedInvalid.Add(1)
	}
}

func (s *Server) handleData(b []byte, src endpoint) {
	sid, _, ok := wire.ParseRelayData(b)
	if !ok {
		return
	}
	s.mu.RLock()
	sess := s.sessions[sid]
	var me *party
	var dst endpoint
	hasDst := false
	if sess != nil {
		for i, p := range sess.parties {
			if p != nil && p.ep.equal(src) {
				me = p
				if o := sess.parties[1-i]; o != nil {
					dst, hasDst = o.ep, true
				}
				break
			}
		}
	}
	s.mu.RUnlock()
	if me == nil {
		s.metrics.droppedUnregistered.Add(1)
		return
	}
	now := time.Now().UnixNano()
	me.lastSeen.Store(now)
	if !hasDst {
		s.metrics.droppedNoPeer.Add(1)
		return
	}
	if !sess.bucket.allow(len(b), now, s.cfg.MaxSessionKbps) {
		s.metrics.droppedRate.Add(1)
		return
	}
	s.metrics.forwardedPackets.Add(1)
	s.metrics.forwardedBytes.Add(uint64(len(b)))
	s.send(dst, b)
}

func (s *Server) handleControl(b []byte, src endpoint) {
	m, err := wire.ParseRelayControl(b)
	if err != nil {
		if len(b) >= 2 && b[1] != wire.RelayVersion {
			s.send(src, wire.AppendReject(nil, wire.SessionID{}, wire.RejectVersion))
		}
		s.metrics.droppedInvalid.Add(1)
		return
	}
	switch m.Type {
	case wire.RelayRegister:
		s.register(m, src)
	case wire.RelayUnregister:
		s.unregister(m, src)
	case wire.RelayPing:
		s.send(src, wire.AppendPong(nil, m.Nonce, src.addr()))
	case wire.RelayEchoRequest:
		s.echo(m, src)
	default:
		s.metrics.droppedInvalid.Add(1)
	}
}

// staleParty is how long a silent party keeps its slot when a new party
// with a different token wants to join a full session.
func (s *Server) staleParty() int64 { return int64(s.cfg.IdleTimeout / 4) }

func (s *Server) register(m wire.RelayControl, src endpoint) {
	now := time.Now().UnixNano()
	var notify endpoint
	doNotify := false
	reject := byte(0)
	present := false

	s.mu.Lock()
	sess := s.sessions[m.Session]
	switch {
	case sess == nil:
		if len(s.sessions) >= s.cfg.MaxSessions {
			reject = wire.RejectBusy
			break
		}
		if s.perIP[src.ip()] >= s.cfg.MaxSessionsPerIP {
			reject = wire.RejectSessionLimit
			break
		}
		sess = &session{id: m.Session}
		sess.parties[0] = s.newParty(m.Session, m.Token, src, now)
		s.sessions[m.Session] = sess
		s.metrics.registrations.Add(1)
	default:
		idx := -1
		for i, p := range sess.parties {
			if p != nil && p.token == m.Token {
				idx = i
			}
		}
		if idx >= 0 {
			p := sess.parties[idx]
			if !p.ep.equal(src) {
				if p.ep.ip() != src.ip() {
					if s.perIP[src.ip()] >= s.cfg.MaxSessionsPerIP {
						reject = wire.RejectSessionLimit
						break
					}
					s.decIP(p.ep.ip())
					s.perIP[src.ip()]++
				}
				s.untrackTCP(p.ep, sess.id)
				p.ep = src
				s.trackTCP(src, sess.id)
			}
			p.lastSeen.Store(now)
			present = sess.parties[1-idx] != nil
			break
		}
		free := -1
		for i, p := range sess.parties {
			if p == nil {
				free = i
				break
			}
		}
		if free < 0 {
			for i, p := range sess.parties {
				if now-p.lastSeen.Load() > s.staleParty() {
					s.removePartyLocked(sess, i)
					free = i
					break
				}
			}
		}
		if free < 0 {
			reject = wire.RejectSessionFull
			break
		}
		if s.perIP[src.ip()] >= s.cfg.MaxSessionsPerIP {
			reject = wire.RejectSessionLimit
			break
		}
		sess.parties[free] = s.newParty(m.Session, m.Token, src, now)
		s.metrics.registrations.Add(1)
		if o := sess.parties[1-free]; o != nil {
			present = true
			notify, doNotify = o.ep, true
		}
	}
	s.mu.Unlock()

	if reject != 0 {
		s.metrics.rejects.Add(1)
		s.send(src, wire.AppendReject(nil, m.Session, reject))
		return
	}
	s.send(src, wire.AppendRegisterAck(nil, m.Session, present, src.addr()))
	if doNotify {
		s.send(notify, wire.AppendPeerJoined(nil, m.Session))
	}
}

// newParty must be called with s.mu held.
func (s *Server) newParty(sid wire.SessionID, t wire.Token, ep endpoint, now int64) *party {
	p := &party{token: t, ep: ep}
	p.lastSeen.Store(now)
	s.perIP[ep.ip()]++
	s.trackTCP(ep, sid)
	return p
}

// trackTCP records that a TCP connection holds a party; s.mu must be held.
func (s *Server) trackTCP(ep endpoint, sid wire.SessionID) {
	if ep.tcp != nil {
		ep.tcp.sessions[sid] = struct{}{}
	}
}

func (s *Server) untrackTCP(ep endpoint, sid wire.SessionID) {
	if ep.tcp != nil {
		delete(ep.tcp.sessions, sid)
	}
}

func (s *Server) decIP(ip netip.Addr) {
	if s.perIP[ip] <= 1 {
		delete(s.perIP, ip)
	} else {
		s.perIP[ip]--
	}
}

// removePartyLocked removes party i; s.mu must be held.
func (s *Server) removePartyLocked(sess *session, i int) {
	p := sess.parties[i]
	if p == nil {
		return
	}
	s.decIP(p.ep.ip())
	s.untrackTCP(p.ep, sess.id)
	sess.parties[i] = nil
	if sess.parties[0] == nil && sess.parties[1] == nil {
		delete(s.sessions, sess.id)
	}
}

func (s *Server) unregister(m wire.RelayControl, src endpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[m.Session]
	if sess == nil {
		return
	}
	for i, p := range sess.parties {
		if p != nil && p.token == m.Token && p.ep.equal(src) {
			s.removePartyLocked(sess, i)
			return
		}
	}
}

func (s *Server) echo(m wire.RelayControl, src endpoint) {
	delay := time.Duration(m.DelaySec) * time.Second
	if delay > s.cfg.MaxEchoDelay {
		delay = s.cfg.MaxEchoDelay
	}
	if s.echoes.Add(1) > 4096 {
		s.echoes.Add(-1)
		s.send(src, wire.AppendReject(nil, wire.SessionID{}, wire.RejectBusy))
		return
	}
	time.AfterFunc(delay, func() {
		defer s.echoes.Add(-1)
		s.send(src, wire.AppendEchoReply(nil, m.Nonce, src.addr()))
	})
}

func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()
	summary := time.NewTicker(5 * time.Minute)
	defer summary.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep()
		case <-summary.C:
			st := s.Status()
			s.log.Info("relay summary", "sessions", st.Sessions, "parties", st.Parties,
				"tcp_conns", st.TCPConns, "forwarded_packets", s.metrics.forwardedPackets.Load())
		}
	}
}

func (s *Server) sweep() {
	cutoff := time.Now().Add(-s.cfg.IdleTimeout).UnixNano()
	s.mu.Lock()
	for _, sess := range s.sessions {
		for i, p := range sess.parties {
			if p != nil && p.lastSeen.Load() < cutoff {
				s.removePartyLocked(sess, i)
				s.metrics.expired.Add(1)
			}
		}
	}
	s.mu.Unlock()
	s.limiter.sweep(time.Now())
}

// Status is a snapshot for the status endpoint.
type Status struct {
	Uptime   string `json:"uptime"`
	Sessions int    `json:"sessions"`
	Parties  int    `json:"parties"`
	TCPConns int    `json:"tcp_conns"`
	Echoes   int64  `json:"pending_echoes"`
}

// Status returns current counts.
func (s *Server) Status() Status {
	s.mu.RLock()
	st := Status{Sessions: len(s.sessions)}
	for _, sess := range s.sessions {
		for _, p := range sess.parties {
			if p != nil {
				st.Parties++
			}
		}
	}
	s.mu.RUnlock()
	st.TCPConns = int(s.metrics.tcpConns.Load())
	st.Echoes = s.echoes.Load()
	st.Uptime = time.Since(s.started).Round(time.Second).String()
	return st
}

// tokenBucket limits bytes per second for one session.
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   int64
	init   bool
}

func (tb *tokenBucket) allow(n int, now int64, kbps int) bool {
	rate := float64(kbps) * 1000 / 8 // bytes per second
	burst := rate                    // one second of traffic
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if !tb.init {
		tb.tokens, tb.last, tb.init = burst, now, true
	}
	tb.tokens += float64(now-tb.last) / 1e9 * rate
	if tb.tokens > burst {
		tb.tokens = burst
	}
	tb.last = now
	if tb.tokens < float64(n) {
		return false
	}
	tb.tokens -= float64(n)
	return true
}

// ipLimiter rate limits control traffic per source IP.
type ipLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[netip.Addr]*ipBucket
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(rate, burst float64) *ipLimiter {
	return &ipLimiter{rate: rate, burst: burst, buckets: make(map[netip.Addr]*ipBucket)}
}

func (l *ipLimiter) allow(ip netip.Addr) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[ip]
	if b == nil {
		if len(l.buckets) > 200000 {
			return false
		}
		b = &ipBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *ipLimiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.buckets {
		if now.Sub(b.last) > time.Minute {
			delete(l.buckets, ip)
		}
	}
}
