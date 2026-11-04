package transport

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Mode is how the client currently reaches the shared relay.
type Mode int32

const (
	ModeDown Mode = iota
	ModeUDP
	ModeTCP
)

func (m Mode) String() string { return [...]string{"down", "udp", "tcp"}[m] }

// ErrRelayDown is returned when no relay transport is available.
var ErrRelayDown = errors.New("relay unreachable")

// RelayOptions configures a RelayClient.
type RelayOptions struct {
	Host    string
	UDPPort int
	TCPPort int

	PingInterval     time.Duration // relay keepalive and RTT probe
	DownAfter        time.Duration // silence after which a transport is down
	UDPRetryInterval time.Duration // UDP probe interval while on TCP
	RegisterInterval time.Duration // session refresh interval

	OnData       func(sid wire.SessionID, payload []byte, viaTCP bool)
	OnPeerJoined func(sid wire.SessionID)
	OnReject     func(sid wire.SessionID, code byte)
	OnModeChange func(Mode)
	OnObserved   func(netip.AddrPort)
	OnEcho       func(nonce uint64, observed netip.AddrPort)
	Logger       *slog.Logger
}

type sessState struct {
	present  bool
	lastSent time.Time
	lastAck  time.Time

	// Register cookie from the relay's last Challenge (protocol 4.3).
	cookie       wire.Cookie
	cookieAt     time.Time
	cookieAnswer time.Time // last Register sent in answer to a Challenge
}

// cookieFresh is how long a Challenge cookie is sent along with refreshes.
// The relay accepts a cookie for at least 30 s.
const cookieFresh = 25 * time.Second

// registerFrame builds the Register for sid, with the session's cookie while
// it is fresh. Callers hold c.mu.
func (c *RelayClient) registerFrame(sid wire.SessionID, s *sessState, now time.Time) []byte {
	if s != nil && !s.cookieAt.IsZero() && now.Sub(s.cookieAt) < cookieFresh {
		return wire.AppendRegisterCookie(nil, sid, c.token, s.cookie)
	}
	return wire.AppendRegister(nil, sid, c.token)
}

// RelayClient keeps the connection to the shared relay: UDP on the shared
// socket when possible, TCP otherwise, with automatic return to UDP.
type RelayClient struct {
	sock    *Socket
	opts    RelayOptions
	log     *slog.Logger
	token   wire.Token
	udpAddr atomic.Pointer[netip.AddrPort]
	mode    atomic.Int32
	tcp     atomic.Pointer[tcpLink]
	dialing atomic.Bool

	lastUDPPong atomic.Int64
	lastTCPPong atomic.Int64
	rtt         atomic.Int64
	observed    atomic.Pointer[netip.AddrPort]

	mu       sync.Mutex
	sessions map[wire.SessionID]*sessState
	pings    map[uint64]int64 // nonce -> clock.Nanotime at send
	kick     chan struct{}
	started  time.Time
}

// NewRelayClient creates a client that uses sock for UDP.
func NewRelayClient(sock *Socket, opts RelayOptions) *RelayClient {
	if opts.PingInterval <= 0 {
		opts.PingInterval = 5 * time.Second
	}
	if opts.DownAfter <= 0 {
		opts.DownAfter = 12 * time.Second
	}
	if opts.UDPRetryInterval <= 0 {
		opts.UDPRetryInterval = 15 * time.Second
	}
	if opts.RegisterInterval <= 0 {
		opts.RegisterInterval = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	c := &RelayClient{
		sock:     sock,
		opts:     opts,
		log:      opts.Logger,
		sessions: make(map[wire.SessionID]*sessState),
		pings:    make(map[uint64]int64),
		kick:     make(chan struct{}, 1),
	}
	rand.Read(c.token[:])
	return c
}

// Mode returns the current transport.
func (c *RelayClient) Mode() Mode { return Mode(c.mode.Load()) }

// RTT returns the last measured round trip time to the relay.
func (c *RelayClient) RTT() time.Duration { return time.Duration(c.rtt.Load()) }

// Observed returns the public address the relay sees for the UDP socket.
func (c *RelayClient) Observed() netip.AddrPort {
	if p := c.observed.Load(); p != nil {
		return *p
	}
	return netip.AddrPort{}
}

// UDPAddr returns the resolved relay UDP address.
func (c *RelayClient) UDPAddr() netip.AddrPort {
	if p := c.udpAddr.Load(); p != nil {
		return *p
	}
	return netip.AddrPort{}
}

// IsRelayAddr reports whether from is the relay's UDP address.
func (c *RelayClient) IsRelayAddr(from netip.AddrPort) bool {
	p := c.udpAddr.Load()
	return p != nil && *p == from
}

// PeerPresent reports whether the other party of sid is registered.
func (c *RelayClient) PeerPresent(sid wire.SessionID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[sid]
	return s != nil && s.present
}

// Register adds a session and announces it to the relay.
func (c *RelayClient) Register(sid wire.SessionID) {
	c.mu.Lock()
	if _, ok := c.sessions[sid]; !ok {
		c.sessions[sid] = &sessState{}
	}
	c.mu.Unlock()
	c.wake()
}

// Unregister removes a session.
func (c *RelayClient) Unregister(sid wire.SessionID) {
	c.mu.Lock()
	_, ok := c.sessions[sid]
	delete(c.sessions, sid)
	c.mu.Unlock()
	if ok {
		c.sendFrame(wire.AppendUnregister(nil, sid, c.token))
	}
}

// Refresh registers sid again at once instead of at the next refresh time.
// A session the relay rejected (SessionFull) gets another try this way.
func (c *RelayClient) Refresh(sid wire.SessionID) {
	c.mu.Lock()
	if s := c.sessions[sid]; s != nil {
		s.lastSent = time.Time{}
	}
	c.mu.Unlock()
	c.wake()
}

func (c *RelayClient) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

var framePool = sync.Pool{New: func() any { b := make([]byte, 0, wire.MaxPacket+64); return &b }}

// SendData sends payload to the other party of sid.
func (c *RelayClient) SendData(sid wire.SessionID, payload []byte) error {
	bp := framePool.Get().(*[]byte)
	frame := wire.AppendRelayData((*bp)[:0], sid, payload)
	err := c.sendFrame(frame)
	*bp = frame
	framePool.Put(bp)
	return err
}

// SendEchoRequest asks the relay for a delayed reply on UDP (natprobe).
func (c *RelayClient) SendEchoRequest(nonce uint64, delaySec byte) error {
	a := c.UDPAddr()
	if !a.IsValid() {
		return ErrRelayDown
	}
	return c.sock.WriteTo(wire.AppendEchoRequest(nil, nonce, delaySec), a)
}

func (c *RelayClient) sendFrame(frame []byte) error {
	switch c.Mode() {
	case ModeUDP:
		return c.sock.WriteTo(frame, c.UDPAddr())
	case ModeTCP:
		if t := c.tcp.Load(); t != nil {
			return t.send(frame)
		}
	}
	return ErrRelayDown
}

func (c *RelayClient) setMode(m Mode) {
	old := Mode(c.mode.Swap(int32(m)))
	if old == m {
		return
	}
	c.log.Info("relay transport changed", "from", old.String(), "to", m.String())
	c.mu.Lock()
	for _, s := range c.sessions {
		s.lastSent = time.Time{} // re-register on the new transport
		if m == ModeDown {
			s.present = false
		}
	}
	c.mu.Unlock()
	if m != ModeTCP {
		if t := c.tcp.Swap(nil); t != nil {
			t.close()
		}
	}
	if c.opts.OnModeChange != nil {
		c.opts.OnModeChange(m)
	}
	c.wake()
}

func (c *RelayClient) resolve() {
	if c.opts.Host == "" {
		return
	}
	ua, err := net.ResolveUDPAddr("udp", net.JoinHostPort(c.opts.Host, strconv.Itoa(c.opts.UDPPort)))
	if err != nil {
		c.log.Warn("relay address resolution failed", "err", err)
		return
	}
	ap := ua.AddrPort()
	ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	c.udpAddr.Store(&ap)
}

func (c *RelayClient) ping(viaTCP bool) {
	var b [8]byte
	rand.Read(b[:])
	nonce := binary.BigEndian.Uint64(b[:])
	now := clock.Nanotime()
	c.mu.Lock()
	c.pings[nonce] = now
	for n, t := range c.pings {
		if now-t > int64(time.Minute) {
			delete(c.pings, n)
		}
	}
	c.mu.Unlock()
	f := wire.AppendPing(nil, nonce)
	if viaTCP {
		if t := c.tcp.Load(); t != nil {
			t.send(f)
		}
		return
	}
	if a := c.UDPAddr(); a.IsValid() {
		c.sock.WriteTo(f, a)
	}
}

// Run maintains the relay connection until ctx is cancelled.
func (c *RelayClient) Run(ctx context.Context) {
	if c.opts.Host == "" {
		c.log.Warn("no relay configured")
		<-ctx.Done()
		return
	}
	c.started = time.Now()
	c.resolve()
	tick := c.opts.PingInterval / 4
	if tick > 500*time.Millisecond {
		tick = 500 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastUDPPing, lastTCPPing, lastResolve time.Time
	for {
		now := time.Now()
		if now.Sub(lastResolve) > 5*time.Minute || !c.UDPAddr().IsValid() {
			c.resolve()
			lastResolve = now
		}
		udpAlive := now.UnixNano()-c.lastUDPPong.Load() < int64(c.opts.DownAfter)
		udpInterval := c.opts.PingInterval
		if !udpAlive && c.Mode() == ModeTCP {
			udpInterval = c.opts.UDPRetryInterval
		}
		if !udpAlive && c.Mode() != ModeTCP && now.Sub(c.started) < c.opts.DownAfter {
			udpInterval = c.opts.PingInterval / 2 // keep trying quickly at startup
		}
		if now.Sub(lastUDPPing) >= udpInterval {
			c.ping(false)
			lastUDPPing = now
		}
		switch {
		case udpAlive:
			if c.Mode() != ModeUDP {
				c.setMode(ModeUDP)
			}
		default:
			// UDP silent. Give it a short grace period at startup, then
			// fall back to TCP if a TCP port is configured.
			grace := c.opts.DownAfter
			if c.lastUDPPong.Load() == 0 && grace > 3*time.Second {
				grace = 3 * time.Second
			}
			if c.Mode() == ModeUDP {
				c.setMode(ModeDown)
			}
			if c.opts.TCPPort > 0 && now.Sub(c.started) >= grace && c.tcp.Load() == nil && !c.dialing.Load() {
				c.dialing.Store(true)
				go c.dialTCP(ctx)
			}
			if tl := c.tcp.Load(); tl != nil {
				if now.Sub(lastTCPPing) >= c.opts.PingInterval {
					c.ping(true)
					lastTCPPing = now
				}
				last := c.lastTCPPong.Load()
				if last == 0 {
					last = tl.opened.UnixNano()
				}
				if now.UnixNano()-last > int64(c.opts.DownAfter) {
					c.log.Warn("relay TCP fallback silent; reconnecting")
					c.tcp.Store(nil)
					tl.close()
					c.setMode(ModeDown)
				}
			}
		}
		c.refreshRegistrations(now)
		select {
		case <-ctx.Done():
			if tl := c.tcp.Swap(nil); tl != nil {
				tl.close()
			}
			return
		case <-t.C:
		case <-c.kick:
		}
	}
}

func (c *RelayClient) refreshRegistrations(now time.Time) {
	if c.Mode() == ModeDown {
		return
	}
	var due [][]byte
	c.mu.Lock()
	for sid, s := range c.sessions {
		if now.Sub(s.lastSent) >= c.opts.RegisterInterval {
			s.lastSent = now
			due = append(due, c.registerFrame(sid, s, now))
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		c.sendFrame(f)
	}
}

func (c *RelayClient) dialTCP(ctx context.Context) {
	defer c.dialing.Store(false)
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.opts.Host, strconv.Itoa(c.opts.TCPPort)))
	if err != nil {
		c.log.Warn("relay TCP fallback unreachable", "err", err)
		time.Sleep(2 * time.Second)
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
	}
	tl := &tcpLink{conn: conn, out: make(chan []byte, 128), closed: make(chan struct{}), opened: time.Now()}
	go tl.writeLoop()
	go func() {
		tl.readLoop(c)
		if c.tcp.CompareAndSwap(tl, nil) && c.Mode() == ModeTCP {
			c.setMode(ModeDown)
		}
	}()
	c.lastTCPPong.Store(0)
	c.tcp.Store(tl)
	if c.Mode() != ModeUDP {
		c.setMode(ModeTCP)
		c.ping(true)
	} else {
		c.tcp.Store(nil)
		tl.close()
	}
}

// HandleFrame processes a frame received from the relay.
func (c *RelayClient) HandleFrame(b []byte, viaTCP bool) {
	switch wire.Classify(b) {
	case wire.ClassRelayData:
		sid, payload, ok := wire.ParseRelayData(b)
		if ok && c.opts.OnData != nil {
			c.opts.OnData(sid, payload, viaTCP)
		}
	case wire.ClassRelayControl:
		m, err := wire.ParseRelayControl(b)
		if err != nil {
			return
		}
		c.handleControl(m, viaTCP)
	}
}

func (c *RelayClient) setObserved(a netip.AddrPort) {
	if !a.IsValid() {
		return
	}
	a = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	old := c.observed.Swap(&a)
	if (old == nil || *old != a) && c.opts.OnObserved != nil {
		c.opts.OnObserved(a)
	}
}

func (c *RelayClient) handleControl(m wire.RelayControl, viaTCP bool) {
	now := time.Now()
	switch m.Type {
	case wire.RelayPong:
		c.mu.Lock()
		sent, ok := c.pings[m.Nonce]
		delete(c.pings, m.Nonce)
		c.mu.Unlock()
		if !ok {
			return
		}
		c.rtt.Store(clock.Nanotime() - sent)
		if viaTCP {
			c.lastTCPPong.Store(now.UnixNano())
		} else {
			// Publish the observed address before marking UDP alive so that
			// anyone reacting to the mode change sees it.
			c.setObserved(m.Addr)
			c.lastUDPPong.Store(now.UnixNano())
			if c.Mode() != ModeUDP {
				c.wake()
			}
		}
	case wire.RelayRegisterAck:
		c.mu.Lock()
		if s := c.sessions[m.Session]; s != nil {
			s.present = m.PeerPresent
			s.lastAck = now
		}
		c.mu.Unlock()
		if !viaTCP {
			c.setObserved(m.Addr)
		}
	case wire.RelayPeerJoined:
		c.mu.Lock()
		s := c.sessions[m.Session]
		if s != nil {
			s.present = true
		}
		c.mu.Unlock()
		if s != nil && c.opts.OnPeerJoined != nil {
			c.opts.OnPeerJoined(m.Session)
		}
	case wire.RelayReject:
		c.log.Warn("relay rejected request", "code", m.Code)
		if c.opts.OnReject != nil {
			c.opts.OnReject(m.Session, m.Code)
		}
	case wire.RelayEchoReply:
		if c.opts.OnEcho != nil {
			c.opts.OnEcho(m.Nonce, m.Addr)
		}
	case wire.RelayChallenge:
		// The relay is under load and wants proof that we receive at our
		// address: repeat the Register with its cookie at once. At most one
		// answer per session and second, so a confused relay cannot make us
		// loop.
		c.mu.Lock()
		s := c.sessions[m.Session]
		if s == nil || now.Sub(s.cookieAnswer) < time.Second {
			c.mu.Unlock()
			return
		}
		s.cookie, s.cookieAt, s.cookieAnswer = m.Cookie, now, now
		s.lastSent = now
		f := c.registerFrame(m.Session, s, now)
		c.mu.Unlock()
		c.log.Debug("relay asked for a register cookie")
		if viaTCP {
			if t := c.tcp.Load(); t != nil {
				t.send(f)
			}
			return
		}
		if a := c.UDPAddr(); a.IsValid() {
			c.sock.WriteTo(f, a)
		}
	}
}

type tcpLink struct {
	conn   net.Conn
	out    chan []byte
	closed chan struct{}
	once   sync.Once
	opened time.Time
}

func (t *tcpLink) send(frame []byte) error {
	select {
	case <-t.closed:
		return ErrRelayDown
	default:
	}
	b := wire.AppendStreamFrame(make([]byte, 0, len(frame)+2), frame)
	select {
	case t.out <- b:
		return nil
	default:
		return errors.New("relay TCP queue full")
	}
}

func (t *tcpLink) writeLoop() {
	w := bufio.NewWriterSize(t.conn, 16*1024)
	for {
		select {
		case <-t.closed:
			return
		case f := <-t.out:
			t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := w.Write(f); err != nil {
				t.close()
				return
			}
			if len(t.out) == 0 {
				if err := w.Flush(); err != nil {
					t.close()
					return
				}
			}
		}
	}
}

func (t *tcpLink) readLoop(c *RelayClient) {
	defer t.close()
	r := bufio.NewReaderSize(t.conn, 16*1024)
	buf := make([]byte, wire.MaxStreamFrame)
	for {
		f, err := wire.ReadStreamFrame(r, buf)
		if err != nil {
			return
		}
		c.HandleFrame(f, true)
	}
}

func (t *tcpLink) close() {
	t.once.Do(func() {
		close(t.closed)
		t.conn.Close()
	})
}
