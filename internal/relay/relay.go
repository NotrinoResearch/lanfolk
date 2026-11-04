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
	UDPReaders       int // goroutines reading the UDP socket (default 1)
	Logger           *slog.Logger

	// MaxIPKbps caps the relay data one source IP (IPv6: one /64) may send,
	// over all its sessions. 0 disables the cap.
	MaxIPKbps int
	// MaxTotalMbps caps all forwarded relay data, so that the relay cannot
	// saturate a host it shares with other services. 0 disables the cap.
	MaxTotalMbps int
	// MaxTCPConns caps open TCP fallback connections in total.
	MaxTCPConns int
	// TCPGrace closes a TCP connection that holds no session and has sent
	// nothing for this long.
	TCPGrace time.Duration
	// MaxEchoesPerIP caps pending delayed echoes per source IP.
	MaxEchoesPerIP int
	// Challenge selects when a UDP Register that would create a new session
	// or party must carry a cookie (see ChallengeMode).
	Challenge ChallengeMode
	// ChallengeSessionsPct switches cookies on while the session table is at
	// least this full (percent of MaxSessions). 0 disables this trigger.
	ChallengeSessionsPct int
	// ChallengeNewPerSec switches cookies on while more than this many new
	// sessions or parties are requested over UDP per second. 0 disables
	// this trigger.
	ChallengeNewPerSec int
}

// ChallengeMode controls the Register cookie (protocol section 4.3).
type ChallengeMode int

const (
	// ChallengeAuto asks for cookies only while the relay is under pressure.
	ChallengeAuto ChallengeMode = iota
	// ChallengeOff never asks for cookies.
	ChallengeOff
	// ChallengeAlways asks for a cookie for every new session or party
	// registered over UDP. Clients before release 0.4 cannot register then.
	ChallengeAlways
)

// ParseChallengeMode parses "auto", "off" or "always".
func ParseChallengeMode(s string) (ChallengeMode, error) {
	switch s {
	case "", "auto":
		return ChallengeAuto, nil
	case "off":
		return ChallengeOff, nil
	case "always":
		return ChallengeAlways, nil
	}
	return ChallengeAuto, fmt.Errorf("challenge mode must be auto, off or always, not %q", s)
}

// pressureHold keeps cookies switched on for this long after the last
// trigger, so that the mode does not flap during an attack.
const pressureHold = 30 * time.Second

// maxPendingEchoes bounds scheduled delayed echoes in total.
const maxPendingEchoes = 4096

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

		MaxIPKbps:            20000,
		MaxTotalMbps:         400,
		MaxTCPConns:          2048,
		TCPGrace:             15 * time.Second,
		MaxEchoesPerIP:       8,
		Challenge:            ChallengeAuto,
		ChallengeSessionsPct: 50,
		ChallengeNewPerSec:   1000,
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
	tcpConns map[*tcpConn]struct{}
	limiter  *ipBuckets // control and STUN messages per IP
	ipData   *ipBuckets // relay data bytes per IP; nil = no cap
	total    tokenBucket
	cookies  *cookieMaker
	echoes   atomic.Int64
	echoMu   sync.Mutex
	echoIP   map[netip.Addr]int
	metrics  metrics
	started  time.Time

	// Pressure detection for Register cookies; guarded by mu.
	newWindow     int64 // start of the current one-second window (unix ns)
	newCount      int   // new-party requests over UDP in that window
	pressureUntil int64 // cookies are required until this time (unix ns)
	pressure      atomic.Bool
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
	if cfg.MaxTCPConns <= 0 {
		cfg.MaxTCPConns = def.MaxTCPConns
	}
	if cfg.TCPGrace <= 0 {
		cfg.TCPGrace = def.TCPGrace
	}
	if cfg.MaxEchoesPerIP <= 0 {
		cfg.MaxEchoesPerIP = def.MaxEchoesPerIP
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
		tcpConns: make(map[*tcpConn]struct{}),
		limiter:  newIPBuckets(cfg.ControlPerIPRate, cfg.ControlPerIPRate*3),
		cookies:  newCookieMaker(),
		echoIP:   make(map[netip.Addr]int),
		started:  time.Now(),
	}
	if cfg.MaxIPKbps > 0 {
		rate := float64(cfg.MaxIPKbps) * 1000 / 8 // bytes per second
		s.ipData = newIPBuckets(rate, rate)       // burst of one second
	}
	ua, err := net.ResolveUDPAddr("udp", cfg.UDPAddr)
	if err != nil {
		return nil, fmt.Errorf("udp address: %w", err)
	}
	if s.udp, err = net.ListenUDP("udp", ua); err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}
	// About 30 ms of traffic at full load: under overload the kernel drops
	// packets instead of queueing them for hundreds of milliseconds.
	s.udp.SetReadBuffer(1 << 20)
	s.udp.SetWriteBuffer(1 << 20)
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
	// Optional extra readers on the same socket (RELAY_UDP_READERS). More
	// than one can reorder packets of a flow slightly; WireGuard's replay
	// window and game protocols tolerate that. One reader is the default
	// until measurements on the deployment host show a gain.
	readers := s.cfg.UDPReaders
	if readers <= 0 {
		readers = 1
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.udpLoop() }()
	}
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
		if !s.limiter.allow(src.ip(), 1, time.Now().UnixNano()) {
			s.metrics.droppedRate.Add(1)
			return
		}
		s.handleControl(b, src)
	case wire.ClassSTUN:
		if !s.limiter.allow(src.ip(), 1, time.Now().UnixNano()) {
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
	// The per-IP cap comes first, so a flooding source spends neither its
	// sessions' budget nor the relay's total budget.
	if s.ipData != nil && !s.ipData.allow(src.ip(), float64(len(b)), now) {
		s.metrics.droppedIPRate.Add(1)
		return
	}
	if !sess.bucket.allow(len(b), now, s.cfg.MaxSessionKbps) {
		s.metrics.droppedRate.Add(1)
		return
	}
	if s.cfg.MaxTotalMbps > 0 && !s.total.allow(len(b), now, s.cfg.MaxTotalMbps*1000) {
		s.metrics.droppedTotalRate.Add(1)
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
	challenge := false
	srcKey := limitKey(src.ip())

	s.mu.Lock()
	sess := s.sessions[m.Session]
	var known *party
	if sess != nil {
		for _, p := range sess.parties {
			if p != nil && p.token == m.Token {
				known = p
			}
		}
	}
	// A Register that would create a new session or a new party must carry
	// a valid cookie while the relay is under pressure (UDP only: a TCP
	// connection has already proven its source address).
	if known == nil && src.tcp == nil && s.needCookieLocked(now) {
		if !m.HasCookie || !s.cookies.valid(src.udp, m.Session, m.Cookie, now/1e9) {
			if m.HasCookie {
				s.metrics.cookieRejects.Add(1)
			}
			challenge = true
		}
	}
	switch {
	case challenge:
	case sess == nil:
		if len(s.sessions) >= s.cfg.MaxSessions {
			reject = wire.RejectBusy
			break
		}
		if s.perIP[srcKey] >= s.cfg.MaxSessionsPerIP {
			reject = wire.RejectSessionLimit
			break
		}
		sess = &session{id: m.Session}
		sess.parties[0] = s.newParty(m.Session, m.Token, src, now)
		s.sessions[m.Session] = sess
		s.metrics.registrations.Add(1)
	case known != nil:
		idx := 0
		if sess.parties[1] == known {
			idx = 1
		}
		p := known
		if !p.ep.equal(src) {
			if oldKey := limitKey(p.ep.ip()); oldKey != srcKey {
				if s.perIP[srcKey] >= s.cfg.MaxSessionsPerIP {
					reject = wire.RejectSessionLimit
					break
				}
				s.decIP(oldKey)
				s.perIP[srcKey]++
			}
			s.untrackTCP(p.ep, sess.id)
			p.ep = src
			s.trackTCP(src, sess.id)
		}
		p.lastSeen.Store(now)
		present = sess.parties[1-idx] != nil
	default:
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
		if s.perIP[srcKey] >= s.cfg.MaxSessionsPerIP {
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

	if challenge {
		s.metrics.challenges.Add(1)
		s.send(src, wire.AppendChallenge(nil, m.Session, s.cookies.current(src.udp, m.Session, now/1e9)))
		return
	}
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

// needCookieLocked counts a request for a new session or party over UDP and
// reports whether it must carry a cookie. s.mu must be held.
func (s *Server) needCookieLocked(now int64) bool {
	switch s.cfg.Challenge {
	case ChallengeOff:
		return false
	case ChallengeAlways:
		return true
	}
	if now-s.newWindow >= int64(time.Second) {
		s.newWindow, s.newCount = now, 0
	}
	s.newCount++
	trigger := s.cfg.ChallengeNewPerSec > 0 && s.newCount > s.cfg.ChallengeNewPerSec
	if pct := s.cfg.ChallengeSessionsPct; pct > 0 && len(s.sessions)*100 >= s.cfg.MaxSessions*pct {
		trigger = true
	}
	if trigger {
		if s.pressure.CompareAndSwap(false, true) {
			s.log.Warn("relay under pressure: new UDP registrations need a cookie",
				"sessions", len(s.sessions), "new_per_sec", s.newCount)
		}
		s.pressureUntil = now + int64(pressureHold)
	}
	return s.pressureOnLocked(now)
}

// pressureOnLocked reports whether cookies are required at now and logs the
// end of a pressure period. s.mu must be held.
func (s *Server) pressureOnLocked(now int64) bool {
	on := now < s.pressureUntil
	if !on && s.pressure.CompareAndSwap(true, false) {
		s.log.Info("relay pressure ended: cookies no longer required")
	}
	return on
}

// newParty must be called with s.mu held.
func (s *Server) newParty(sid wire.SessionID, t wire.Token, ep endpoint, now int64) *party {
	p := &party{token: t, ep: ep}
	p.lastSeen.Store(now)
	s.perIP[limitKey(ep.ip())]++
	s.trackTCP(ep, sid)
	return p
}

// trackTCP records that a TCP connection holds a party; s.mu must be held.
func (s *Server) trackTCP(ep endpoint, sid wire.SessionID) {
	if ep.tcp != nil {
		ep.tcp.sessions[sid] = struct{}{}
		ep.tcp.nsess.Store(int32(len(ep.tcp.sessions)))
	}
}

func (s *Server) untrackTCP(ep endpoint, sid wire.SessionID) {
	if ep.tcp != nil {
		delete(ep.tcp.sessions, sid)
		if len(ep.tcp.sessions) == 0 && ep.tcp.nsess.Swap(0) != 0 {
			ep.tcp.lastActive.Store(time.Now().UnixNano())
		}
	}
}

// decIP releases one party of the limit key ip; s.mu must be held.
func (s *Server) decIP(ip netip.Addr) {
	ip = limitKey(ip)
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
	key := limitKey(src.ip())
	s.echoMu.Lock()
	if s.echoIP[key] >= s.cfg.MaxEchoesPerIP || s.echoes.Load() >= maxPendingEchoes {
		s.echoMu.Unlock()
		s.metrics.echoRejects.Add(1)
		s.send(src, wire.AppendReject(nil, wire.SessionID{}, wire.RejectBusy))
		return
	}
	s.echoIP[key]++
	s.echoes.Add(1)
	s.echoMu.Unlock()
	time.AfterFunc(delay, func() {
		s.echoMu.Lock()
		if s.echoIP[key] <= 1 {
			delete(s.echoIP, key)
		} else {
			s.echoIP[key]--
		}
		s.echoes.Add(-1)
		s.echoMu.Unlock()
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
	s.pressureOnLocked(time.Now().UnixNano())
	s.mu.Unlock()
	s.tcpMu.Lock()
	conns := make([]*tcpConn, 0, len(s.tcpConns))
	for c := range s.tcpConns {
		conns = append(conns, c)
	}
	s.tcpMu.Unlock()
	now := time.Now().UnixNano()
	for _, c := range conns {
		c.closeIfIdle(now)
	}
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
