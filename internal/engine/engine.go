// Package engine is the Lanfolk client core. It ties identity, friends,
// private networks, relays, path selection, member relaying, the WireGuard
// tunnel, LAN behavior and fallback discovery together. The Windows service
// and Linux test clients wrap it; the UI and CLI talk to it over internal/ipc.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/invite"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/transport"
	"github.com/NotrinoResearch/lanfolk/internal/version"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.zx2c4.com/wireguard/tun"
)

// Adapter is the OS configuration of the virtual adapter (internal/winnet).
type Adapter interface {
	Configure(addr netip.Prefix, mtu int) error
	SetCompatMode(on bool) error
	SetPrivate() error
	Close() error
}

// Config configures an Engine.
type Config struct {
	DataDir string
	Logger  *slog.Logger
	// ListenAddr overrides the UDP listen address (tests use 127.0.0.1:0).
	ListenAddr string
	// NewTUN creates the packet device; nil runs without a data plane.
	NewTUN func(name string, mtu int) (tun.Device, error)
	// NewAdapter configures the OS side of the device; nil skips it.
	NewAdapter func(tun.Device) (Adapter, error)
	// Blocked is the port denylist used inside the tunnel.
	Blocked func(proto uint8, port uint16) bool
	// Protector protects the identity seed; nil uses the platform default.
	Protector identity.Protector
	// DefaultRelays overrides the build-time default relay.
	DefaultRelays []store.Relay
	// StartCapture starts global broadcast capture (Windows capture mode);
	// nil disables it. It must call deliver for each captured IPv4 packet.
	StartCapture func(ctx context.Context, deliver func([]byte)) (stop func(), err error)
	// InviteTTL overrides how long new invite codes are valid (tests).
	InviteTTL time.Duration
	// Test hooks.
	LoopbackCandidates bool
	DisablePortMapping bool
	DisableDHT         bool
	DHTBootstrap       []string
	PathConfig         *pathmgr.Config
	RelayOptions       func(*transport.RelayOptions)
	TickInterval       time.Duration
	HelloInterval      time.Duration
	OfflineAfter       time.Duration
	DHTAfter           time.Duration
	// OnEvent receives UI events (internal/ipc event names).
	OnEvent func(event string, data any)
}

// MTU is the virtual adapter MTU (see the plan's MTU calculation).
const MTU = 1380

// Engine is a running client.
type Engine struct {
	cfg   Config
	log   *slog.Logger
	store *store.Store
	key   *identity.PrivateKey
	sock  *transport.Socket
	bind  *transport.Bind
	boot  uint64
	ctx   context.Context

	mu           sync.Mutex
	peers        map[identity.PublicKey]*peer
	lists        map[network.ID]*network.List
	active       *network.List
	relays       []*relayConn
	tunnel       *tunnel
	tunErr       string
	cands        []candidate
	candGen      uint32
	candAt       time.Time
	lastOffer    time.Time
	relayLimiter relayLimiter
	nick         string
	settings     store.Settings
	// Lock-free copies of settings read on the packet path.
	relayOnlyA  atomic.Bool
	manualPortA atomic.Int32

	byKey   atomic.Pointer[map[identity.PublicKey]*peer]
	bySID   atomic.Pointer[map[wire.SessionID]*peer]
	byKeyID atomic.Pointer[map[identity.KeyID]*peer]
	byAddr  atomic.Pointer[map[netip.AddrPort]*peer]
	relayIx atomic.Pointer[[]*relayConn]

	// Invites (invite.go). All guarded by mu except inviteIx and the
	// counters.
	owned      map[network.ID]invite.Code  // invites of networks we own
	pending    map[network.ID]*pendingJoin // codes we redeemed
	invSess    map[wire.SessionID]*inviteSession
	inviteIx   atomic.Pointer[map[wire.SessionID]*inviteSession]
	inviteSeq  atomic.Uint64
	hasPending atomic.Bool // some friend has networks waiting for confirmation
	joinMu     sync.Mutex
	joinTimes  map[network.ID][]time.Time

	portmap *portMapper
	dht     *dhtFallback
	wg      sync.WaitGroup
}

// New loads or creates the identity and opens the socket. Call Run next.
func New(cfg Config) (*Engine, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Protector == nil {
		cfg.Protector = identity.DefaultProtector()
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 50 * time.Millisecond
	}
	if cfg.HelloInterval <= 0 {
		cfg.HelloInterval = 25 * time.Second
	}
	if cfg.OfflineAfter <= 0 {
		cfg.OfflineAfter = 60 * time.Second
	}
	if cfg.DHTAfter <= 0 {
		cfg.DHTAfter = 30 * time.Second
	}
	if cfg.InviteTTL <= 0 {
		cfg.InviteTTL = invite.TTL
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open data store: %w", err)
	}
	e := &Engine{cfg: cfg, log: cfg.Logger, store: st,
		peers: make(map[identity.PublicKey]*peer), lists: make(map[network.ID]*network.List),
		owned: make(map[network.ID]invite.Code), pending: make(map[network.ID]*pendingJoin),
		invSess: make(map[wire.SessionID]*inviteSession), joinTimes: make(map[network.ID][]time.Time)}
	var b [8]byte
	rand.Read(b[:])
	// Boot IDs grow over time so a receiver can reject frames from an older
	// run; the random low bits separate restarts within one nanosecond tick.
	e.boot = uint64(time.Now().UnixNano())&^0xFFFF | uint64(binary.BigEndian.Uint16(b[:2]))
	if err := e.loadIdentity(); err != nil {
		return nil, err
	}
	d := st.Data()
	e.settings = d.Settings
	e.syncSettingsCopies()
	e.nick = d.Identity.Nickname
	for _, n := range d.Networks {
		l, err := network.Unmarshal(n.SignedList)
		if err != nil {
			e.log.Warn("ignoring stored network with an invalid member list", "network", n.ID, "err", err)
			continue
		}
		e.lists[l.ID] = l
		if n.Active {
			e.active = l
		}
	}
	e.loadInvites(d)
	if err := e.openSocket(); err != nil {
		return nil, err
	}
	e.bind = transport.NewBind(e, e.sock.Port())
	e.storeMaps()
	if !cfg.DisableDHT {
		e.dht = newDHTFallback(e)
	}
	return e, nil
}

func (e *Engine) loadIdentity() error {
	d := e.store.Data()
	if len(d.Identity.EncryptedPrivateKey) > 0 {
		seed, err := e.cfg.Protector.Unprotect(d.Identity.EncryptedPrivateKey)
		if err != nil {
			return fmt.Errorf("unlock identity: %w", err)
		}
		k, err := identity.FromSeed(seed)
		if err != nil {
			return err
		}
		e.key = k
		return nil
	}
	k, err := identity.Generate()
	if err != nil {
		return err
	}
	blob, err := e.cfg.Protector.Protect(k.Seed())
	if err != nil {
		return fmt.Errorf("protect identity: %w", err)
	}
	if err := e.store.Update(func(d *store.Data) error {
		d.Identity = store.Identity{PublicKey: k.Public().Hex(), EncryptedPrivateKey: blob, Nickname: defaultNickname()}
		return nil
	}); err != nil {
		return err
	}
	e.key = k
	e.log.Info("created a new identity", "key", k.Public().String())
	return nil
}

func defaultNickname() string {
	b := make([]byte, 2)
	rand.Read(b)
	return fmt.Sprintf("Player-%04X", binary.BigEndian.Uint16(b))
}

func (e *Engine) openSocket() error {
	addr := e.cfg.ListenAddr
	if addr == "" {
		port := e.settings.ManualPort
		if port == 0 {
			port = e.settings.ListenPort
		}
		addr = ":" + strconv.Itoa(port)
	}
	s, err := transport.ListenSocket(addr)
	if err != nil && e.cfg.ListenAddr == "" && e.settings.ManualPort == 0 {
		s, err = transport.ListenSocket(":0") // remembered port is taken
	}
	if err != nil {
		return fmt.Errorf("open UDP socket: %w", err)
	}
	e.sock = s
	if e.cfg.ListenAddr == "" && e.settings.ManualPort == 0 && int(s.Port()) != e.settings.ListenPort {
		port := int(s.Port())
		e.store.Update(func(d *store.Data) error { d.Settings.ListenPort = port; return nil })
		e.settings.ListenPort = port
	}
	return nil
}

// PublicKey returns the local identity key.
func (e *Engine) PublicKey() identity.PublicKey { return e.key.Public() }

// Port returns the local UDP port.
func (e *Engine) Port() uint16 { return e.sock.Port() }

// Socket exposes the shared socket (tests install filters on it).
func (e *Engine) Socket() *transport.Socket { return e.sock }

// Run processes traffic until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	// ctx is read under mu by calls that arrive before Run (the control
	// server starts first), so it is set under mu too.
	e.mu.Lock()
	e.ctx = ctx
	e.applyRelaysLocked()
	e.reconcileLocked()
	e.mu.Unlock()
	e.refreshCandidates(true)
	e.mu.Lock()
	if e.active != nil {
		e.startTunnelLocked()
	}
	e.mu.Unlock()
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.sock.Run(e.onPacket) }()
	if !e.cfg.DisablePortMapping {
		pm := startPortMapper(ctx, e)
		e.mu.Lock()
		e.portmap = pm
		e.mu.Unlock()
	}
	e.loop(ctx)
	e.shutdown()
	return nil
}

func (e *Engine) shutdown() {
	e.mu.Lock()
	for _, p := range e.peers {
		e.sendBye(p)
	}
	e.stopTunnelLocked()
	// Free the session slots at once. Otherwise the relay keeps the old
	// registration (a second party for the same session) until it is stale,
	// and a restarted client would be rejected as SessionFull meanwhile.
	for _, r := range e.relays {
		for _, p := range e.peers {
			r.client.Unregister(p.sid)
		}
		for sid := range e.invSess {
			r.client.Unregister(sid)
		}
	}
	for _, r := range e.relays {
		r.stop()
	}
	e.relays = nil
	e.mu.Unlock()
	if e.dht != nil {
		e.dht.stop()
	}
	e.sock.Close()
	e.wg.Wait()
}

func (e *Engine) loop(ctx context.Context) {
	t := time.NewTicker(e.cfg.TickInterval)
	defer t.Stop()
	slow := time.NewTicker(time.Second)
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickPaths()
		case <-slow.C:
			e.tickSlow()
		}
	}
}

// tickSlow runs once a second: presence, Hellos, candidates, relay offers,
// relay list changes, DHT fallback and UI updates.
func (e *Engine) tickSlow() {
	now := time.Now()
	e.mu.Lock()
	peers := e.peerListLocked()
	e.mu.Unlock()
	for _, p := range peers {
		e.peerHousekeeping(p, now)
	}
	e.tickInvites(now)
	e.refreshCandidates(false)
	e.sendRelayOffers(now)
	if e.dht != nil {
		e.dht.tick(now)
	}
	e.publishPeers()
}

func (e *Engine) peerListLocked() []*peer {
	out := make([]*peer, 0, len(e.peers))
	for _, p := range e.peers {
		out = append(out, p)
	}
	return out
}

// onPacket demultiplexes one datagram from the shared socket.
func (e *Engine) onPacket(b []byte, from netip.AddrPort) {
	switch wire.Classify(b) {
	case wire.ClassWireGuard:
		if p := e.peerByAddr(from); p != nil {
			p.noteInbound(e.directPathID(p, from))
			e.bind.Deliver(b, p.ep)
			return
		}
		e.bind.Deliver(b, &transport.RawEndpoint{Addr: from})
	case wire.ClassRelayData, wire.ClassRelayControl:
		if r := e.relayByAddr(from); r != nil {
			r.client.HandleFrame(b, false)
		}
	case wire.ClassControl:
		e.onControlFrame(b, via{kind: pathmgr.Direct, addr: from})
	case wire.ClassMemberForward:
		e.onMemberForward(b, from)
	case wire.ClassMemberDeliver:
		e.onMemberDeliver(b, from)
	}
}

func (e *Engine) relayByAddr(from netip.AddrPort) *relayConn {
	rs := e.relayIx.Load()
	if rs == nil {
		return nil
	}
	for _, r := range *rs {
		if r.client.IsRelayAddr(from) {
			return r
		}
	}
	return nil
}

func (e *Engine) peerByAddr(a netip.AddrPort) *peer {
	if m := e.byAddr.Load(); m != nil {
		return (*m)[a]
	}
	return nil
}

func (e *Engine) peerBySID(s wire.SessionID) *peer {
	if m := e.bySID.Load(); m != nil {
		return (*m)[s]
	}
	return nil
}

func (e *Engine) peerByKey(k identity.PublicKey) *peer {
	if m := e.byKey.Load(); m != nil {
		return (*m)[k]
	}
	return nil
}

func (e *Engine) peerByKeyID(id identity.KeyID) *peer {
	if m := e.byKeyID.Load(); m != nil {
		return (*m)[id]
	}
	return nil
}

// storeMaps rebuilds the lock-free lookup tables; e.mu must be held or the
// engine must not be running yet.
func (e *Engine) storeMaps() {
	bySID := make(map[wire.SessionID]*peer, len(e.peers))
	byKeyID := make(map[identity.KeyID]*peer, len(e.peers))
	byKey := make(map[identity.PublicKey]*peer, len(e.peers))
	for _, p := range e.peers {
		bySID[p.sid] = p
		byKeyID[p.keyID] = p
		byKey[p.key] = p
	}
	e.bySID.Store(&bySID)
	e.byKeyID.Store(&byKeyID)
	e.byKey.Store(&byKey)
	e.rebuildAddrMap()
}

// rebuildAddrMap maps every known direct candidate address to its peer so
// that WireGuard packets from it are attributed to the right peer.
func (e *Engine) rebuildAddrMap() {
	m := make(map[netip.AddrPort]*peer)
	for _, p := range e.peers {
		for _, a := range p.directAddrs() {
			m[a] = p
		}
	}
	e.byAddr.Store(&m)
}

// syncSettingsCopies refreshes the lock-free setting copies; call it after
// e.settings changes.
func (e *Engine) syncSettingsCopies() {
	e.relayOnlyA.Store(e.settings.RelayOnly)
	e.manualPortA.Store(int32(e.settings.ManualPort))
}

func (e *Engine) relayOnly() bool { return e.relayOnlyA.Load() }

func (e *Engine) emit(event string, data any) {
	if e.cfg.OnEvent != nil {
		e.cfg.OnEvent(event, data)
	}
}

func nanotime() int64 { return clock.Nanotime() }

var errNoPath = errors.New("no path to peer")

// listenUDPAddr reports the local address for diagnostics.
func (e *Engine) listenUDPAddr() string {
	return net.JoinHostPort("", strconv.Itoa(int(e.sock.Port())))
}

// protocolVersion is announced in Hello.
const protocolVersion = version.ProtocolVersion
