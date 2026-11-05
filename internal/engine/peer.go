package engine

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/transport"
	"github.com/NotrinoResearch/lanfolk/internal/version"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.zx2c4.com/wireguard/replay"
)

// via says how a frame reached us or how to send one.
type via struct {
	kind    pathmgr.Kind
	addr    netip.AddrPort // direct: peer address; member relay: relaying member's address
	relay   int            // shared relay index
	relayer *peer          // member relay
	pathID  pathmgr.PathID
}

// peer is one remote identity we keep a session with: a friend, a member of
// the active network, or both.
type peer struct {
	e       *Engine
	key     identity.PublicKey
	keyID   identity.KeyID
	pair    *identity.Pair
	sid     wire.SessionID
	sendKey [32]byte
	recvKey [32]byte
	ep      *transport.PeerEndpoint
	counter atomic.Uint64

	// Roles, guarded by Engine.mu.
	isFriend  bool
	confirmed bool
	managed   bool // member of the active network: paths and tunnel
	vip       netip.Addr
	vipp      atomic.Pointer[netip.Addr] // lock-free copy of vip for the LAN layer
	listNick  string
	// appVersion is the peer's Lanfolk release from its last Hello.
	appVersion string

	mu           sync.Mutex
	nick         string
	pm           *pathmgr.Manager
	remoteBoot   uint64
	filter       replay.Filter
	lastAuth     time.Time
	helloSent    time.Time
	helloRecv    time.Time
	remoteActive network.ID
	remoteFlags  uint8
	candGen      uint32
	candSentAt   time.Time
	reasm        *peermsg.Reassembler
	offers       map[identity.KeyID]offer
	pathVer      uint64
	cacheAt      time.Time
	wasOnline    bool
	lastSeenSave time.Time

	active      atomic.Pointer[via]
	inboundPath atomic.Uint32
	inboundAt   atomic.Int64
	stats       atomic.Pointer[pathmgr.Stats]
}

type offer struct {
	rtt time.Duration
	at  time.Time
}

func (e *Engine) newPeer(pk identity.PublicKey) (*peer, error) {
	pair, err := identity.DerivePair(e.key, pk)
	if err != nil {
		return nil, err
	}
	p := &peer{e: e, key: pk, keyID: pk.KeyID(), pair: pair, sid: wire.SessionID(pair.SessionID),
		sendKey: pair.ControlSendKey(), recvKey: pair.ControlRecvKey(), ep: transport.NewPeerEndpoint(pk),
		reasm: peermsg.NewReassembler(10 * time.Second), offers: make(map[identity.KeyID]offer)}
	return p, nil
}

func (p *peer) displayName() string {
	p.mu.Lock()
	n := p.nick
	p.mu.Unlock()
	if n == "" {
		n = p.listNick
	}
	if n == "" {
		n = p.key.String()
	}
	return n
}

func (p *peer) online(now time.Time, after time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.lastAuth.IsZero() && now.Sub(p.lastAuth) < after
}

// noteInbound records that a WireGuard packet arrived on a path; the path
// manager learns about it on the next tick (no lock on the data path).
func (p *peer) noteInbound(id pathmgr.PathID) {
	if id != 0 {
		p.inboundPath.Store(uint32(id))
		p.inboundAt.Store(nanotime())
	}
}

func (p *peer) directAddrs() []netip.AddrPort {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.directAddrsLocked()
}

func (p *peer) directAddrsLocked() []netip.AddrPort {
	if p.pm == nil {
		return nil
	}
	var out []netip.AddrPort
	for _, pi := range p.pm.Paths(0) {
		if pi.Kind == pathmgr.Direct {
			out = append(out, pi.Addr)
		}
	}
	return out
}

func (e *Engine) directPathID(p *peer, from netip.AddrPort) pathmgr.PathID {
	if v := p.active.Load(); v != nil && v.kind == pathmgr.Direct && v.addr == from {
		return v.pathID
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pm == nil {
		return 0
	}
	id, _ := p.pm.FindDirect(from)
	return id
}

// reconcileLocked brings the peer table in line with friends and the active
// network. e.mu must be held.
func (e *Engine) reconcileLocked() {
	d := e.store.Data()
	want := make(map[identity.PublicKey]*peer)
	friendSet := make(map[identity.PublicKey]store.Friend)
	for _, f := range d.Friends {
		pk, err := identity.ParsePublicKeyHex(f.PublicKey)
		if err == nil && pk != e.key.Public() {
			friendSet[pk] = f
		}
	}
	members := make(map[identity.PublicKey]network.Member)
	if e.active != nil {
		for _, m := range e.active.Members {
			if m.PublicKey != e.key.Public() {
				members[m.PublicKey] = m
			}
		}
	}
	ensure := func(pk identity.PublicKey) *peer {
		if p := want[pk]; p != nil {
			return p
		}
		p := e.peers[pk]
		if p == nil {
			np, err := e.newPeer(pk)
			if err != nil {
				e.log.Warn("cannot derive keys for peer", "peer", pk.String(), "err", err)
				return nil
			}
			p = np
			for _, r := range e.relays {
				r.client.Register(p.sid)
			}
		}
		want[pk] = p
		return p
	}
	for pk, f := range friendSet {
		if p := ensure(pk); p != nil {
			p.isFriend = true
			p.confirmed = !f.ConfirmedAt.IsZero()
			if p.listNick == "" {
				p.listNick = f.Nickname
			}
		}
	}
	for pk, m := range members {
		if p := ensure(pk); p != nil {
			p.vip = m.VirtualIP
			v := m.VirtualIP
			p.vipp.Store(&v)
			p.listNick = m.Nickname
		}
	}
	for pk, p := range want {
		_, isMember := members[pk]
		if _, ok := friendSet[pk]; !ok {
			p.isFriend, p.confirmed = false, false
		}
		if isMember && !p.managed {
			e.startManagingLocked(p, d.PeerCache)
		} else if !isMember && p.managed {
			e.stopManagingLocked(p)
		}
	}
	for pk, p := range e.peers {
		if _, ok := want[pk]; !ok {
			if p.managed {
				e.stopManagingLocked(p)
			}
			e.sendBye(p)
			for _, r := range e.relays {
				r.client.Unregister(p.sid)
			}
		}
	}
	e.peers = want
	e.storeMaps()
	e.syncTunnelPeersLocked()
}

func (e *Engine) pathConfig() pathmgr.Config {
	cfg := pathmgr.DefaultConfig()
	if e.cfg.PathConfig != nil {
		cfg = *e.cfg.PathConfig
	}
	cfg.RelayOnly = e.settings.RelayOnly
	return cfg
}

func (e *Engine) startManagingLocked(p *peer, cache []store.PeerCacheEntry) {
	p.managed = true
	now := nanotime()
	p.mu.Lock()
	p.pm = pathmgr.New(e.pathConfig())
	p.pm.SetPeerRelayOnly(p.remoteFlags&peermsg.FlagRelayOnly != 0)
	for i, r := range e.relays {
		p.pm.SetSharedRelay(uint8(i), r.kind(), r.client.PeerPresent(p.sid), now)
	}
	p.candSentAt = time.Time{}
	p.mu.Unlock()
	// Last-known-good addresses first (discovery order step 1).
	for _, c := range cache {
		if c.PublicKey != p.key.Hex() {
			continue
		}
		if a, err := netip.ParseAddrPort(c.Address); err == nil {
			p.mu.Lock()
			p.pm.AddDirect(a, pathmgr.SourceCache, now)
			p.mu.Unlock()
		}
	}
}

func (e *Engine) stopManagingLocked(p *peer) {
	p.managed = false
	p.vip = netip.Addr{}
	p.vipp.Store(nil)
	p.mu.Lock()
	p.pm = nil
	p.mu.Unlock()
	p.active.Store(nil)
}

// sendMsg encrypts and sends a control message to p, on v or on the best
// available path.
func (e *Engine) sendMsg(p *peer, m peermsg.Message, v *via) error {
	plain := peermsg.Marshal(peermsg.Header{Boot: e.boot, Counter: p.counter.Add(1)}, m)
	frame := wire.SealControl(make([]byte, 0, len(plain)+wire.ControlOverhead), &p.sendKey, p.sid, plain)
	if v == nil {
		v = e.bestVia(p)
	}
	if v == nil {
		return errNoPath
	}
	return e.sendVia(p, frame, v)
}

// bestVia is the active path, or a shared relay where the peer is present.
func (e *Engine) bestVia(p *peer) *via {
	if v := p.active.Load(); v != nil {
		return v
	}
	return e.relayVia(p)
}

func (e *Engine) relayVia(p *peer) *via {
	rs := e.relayIx.Load()
	if rs == nil {
		return nil
	}
	var fallback *via
	for i, r := range *rs {
		k := r.kind()
		if k == 0 {
			continue
		}
		v := &via{kind: k, relay: i}
		if r.client.PeerPresent(p.sid) {
			return v
		}
		if fallback == nil {
			fallback = v
		}
	}
	return fallback
}

var memberFramePool = sync.Pool{New: func() any { b := make([]byte, 0, wire.MaxPacket+64); return &b }}

// sendVia transmits a payload (WireGuard packet or control frame) to p.
func (e *Engine) sendVia(p *peer, payload []byte, v *via) error {
	switch v.kind {
	case pathmgr.Direct:
		return e.sock.WriteTo(payload, v.addr)
	case pathmgr.SharedRelay, pathmgr.SharedRelayTCP:
		rs := e.relayIx.Load()
		if rs == nil || v.relay >= len(*rs) {
			return errNoPath
		}
		return (*rs)[v.relay].client.SendData(p.sid, payload)
	case pathmgr.MemberRelay:
		if v.relayer == nil {
			return errNoPath
		}
		bp := memberFramePool.Get().(*[]byte)
		f := wire.AppendMemberForward((*bp)[:0], v.relayer.sid, p.keyID, payload)
		err := e.sock.WriteTo(f, v.addr)
		*bp = f
		memberFramePool.Put(bp)
		return err
	}
	return errNoPath
}

// onControlFrame authenticates and dispatches one control frame.
func (e *Engine) onControlFrame(b []byte, v via) {
	sid, ok := wire.ControlSession(b)
	if !ok {
		return
	}
	p := e.peerBySID(sid)
	if p == nil {
		return
	}
	e.onControlFrom(p, b, v)
}

func (e *Engine) onControlFrom(p *peer, b []byte, v via) {
	if v.kind == pathmgr.Direct && e.relayOnly() {
		// Relay only: nothing is answered from the home address, not even a
		// probe from a member that still knows it.
		return
	}
	plain, err := wire.OpenControl(&p.recvKey, b)
	if err != nil {
		return
	}
	h, msg, err := peermsg.Unmarshal(plain)
	if err != nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	if h.Boot != p.remoteBoot {
		// A new run of the peer has a larger boot ID. An older boot is
		// accepted only after a long silence (for example a clock change).
		if h.Boot < p.remoteBoot && now.Sub(p.lastAuth) < 30*time.Second {
			p.mu.Unlock()
			return
		}
		p.remoteBoot = h.Boot
		p.filter.Reset()
	}
	if !p.filter.ValidateCounter(h.Counter, 1<<62) {
		p.mu.Unlock()
		return
	}
	p.lastAuth = now
	learned := false
	if v.kind == pathmgr.Direct && p.pm != nil && !e.relayOnly() {
		if id, ok := p.pm.FindDirect(v.addr); ok {
			v.pathID = id
		} else if id, fresh := p.pm.AddDirect(v.addr, pathmgr.SourcePeerReflexive, nanotime()); fresh {
			v.pathID = id
			learned = true
		}
	}
	p.mu.Unlock()
	if learned {
		e.mu.Lock()
		e.rebuildAddrMap()
		e.mu.Unlock()
	}
	switch m := msg.(type) {
	case *peermsg.Hello:
		e.onHello(p, m)
	case *peermsg.MembershipUpdate:
		e.onMembershipFragment(p, m)
	case *peermsg.Candidates:
		e.onCandidates(p, m)
	case *peermsg.PathProbe:
		ack := &peermsg.PathProbeAck{ProbeID: m.ProbeID, PathID: m.PathID}
		if v.kind == pathmgr.Direct {
			ack.Observed = v.addr
		}
		e.sendMsg(p, ack, &v)
	case *peermsg.PathProbeAck:
		p.mu.Lock()
		if p.pm != nil {
			p.pm.OnAck(m.ProbeID, nanotime())
		}
		p.mu.Unlock()
	case *peermsg.RelayOffer:
		e.onRelayOffer(p, m)
	case *peermsg.Bye:
		p.mu.Lock()
		p.lastAuth = time.Time{}
		p.mu.Unlock()
	}
}

func (e *Engine) helloFor(p *peer) *peermsg.Hello {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := &peermsg.Hello{ProtocolVersion: protocolVersion, Nickname: e.nick, Capabilities: peermsg.CapLanFrame,
		AppVersion: version.Version}
	if p.isFriend {
		h.Flags |= peermsg.FlagFriend
	}
	if e.settings.RelayOnly {
		h.Flags |= peermsg.FlagRelayOnly
	} else {
		h.Capabilities |= peermsg.CapMemberRelay
	}
	if e.active != nil {
		h.ActiveNetwork = e.active.ID
	}
	for _, l := range e.lists {
		if l.Has(p.key) && len(h.Networks) < peermsg.MaxNetRefs {
			h.Networks = append(h.Networks, peermsg.NetRef{ID: l.ID, Version: l.Version})
		}
	}
	return h
}

func (e *Engine) sendHello(p *peer, wanted ...peermsg.NetRef) {
	h := e.helloFor(p)
	for _, w := range wanted {
		if len(h.Networks) < peermsg.MaxNetRefs {
			h.Networks = append(h.Networks, w)
		}
	}
	p.mu.Lock()
	p.helloSent = time.Now()
	p.mu.Unlock()
	e.sendMsg(p, h, nil)
}

func (e *Engine) sendBye(p *peer) { e.sendMsg(p, &peermsg.Bye{}, nil) }

func (e *Engine) onHello(p *peer, m *peermsg.Hello) {
	now := time.Now()
	p.mu.Lock()
	reply := p.helloRecv.IsZero() || now.Sub(p.helloRecv) > e.cfg.OfflineAfter
	if now.Sub(p.helloSent) < time.Second {
		reply = false
	}
	p.helloRecv = now
	renamed := p.nick != m.Nickname
	p.nick = m.Nickname
	p.appVersion = m.AppVersion
	p.remoteActive = m.ActiveNetwork
	wasRelayOnly := p.remoteFlags&peermsg.FlagRelayOnly != 0
	p.remoteFlags = m.Flags
	peerRelayOnly := m.Flags&peermsg.FlagRelayOnly != 0
	dropDirect := p.pm != nil && peerRelayOnly && len(p.directAddrsLocked()) > 0
	if p.pm != nil {
		p.pm.SetPeerRelayOnly(peerRelayOnly)
	}
	if wasRelayOnly && !peerRelayOnly {
		p.candSentAt = time.Time{} // the peer takes direct paths again: offer ours now
	}
	p.mu.Unlock()
	if dropDirect {
		e.mu.Lock()
		e.rebuildAddrMap()
		e.mu.Unlock()
	}

	// Mutual friendship: we added them and they say they added us.
	e.mu.Lock()
	confirm := p.isFriend && !p.confirmed && m.Flags&peermsg.FlagFriend != 0
	var push []*network.List
	var wanted []peermsg.NetRef
	for _, ref := range m.Networks {
		l := e.lists[ref.ID]
		switch {
		case l != nil && l.Version > ref.Version:
			push = append(push, l)
		case l == nil || l.Version < ref.Version:
			ver := uint64(0)
			if l != nil {
				ver = l.Version
			}
			wanted = append(wanted, peermsg.NetRef{ID: ref.ID, Version: ver})
		}
	}
	e.mu.Unlock()
	if confirm {
		e.confirmFriend(p)
	}
	if renamed {
		// Member lists and friends show the announced nickname.
		go e.publishNetworks()
		go e.publishFriends()
	}
	e.noteSeen(p, now)
	for _, l := range push {
		e.sendList(p, l)
	}
	if reply || len(wanted) > 0 {
		e.sendHello(p, wanted...)
	}
}

func (e *Engine) confirmFriend(p *peer) {
	now := time.Now().UTC()
	e.store.Update(func(d *store.Data) error {
		for i := range d.Friends {
			if d.Friends[i].PublicKey == p.key.Hex() && d.Friends[i].ConfirmedAt.IsZero() {
				d.Friends[i].ConfirmedAt = now
			}
		}
		return nil
	})
	e.mu.Lock()
	p.confirmed = true
	e.mu.Unlock()
	e.log.Info("friendship is now active", "peer", p.displayName())
	e.publishFriends()
	// An invited friend joins the networks it asked for.
	e.applyPendingMembers()
	// The friend now gets FlagFriend in our Hello too.
	e.sendHello(p)
}

func (e *Engine) noteSeen(p *peer, now time.Time) {
	p.mu.Lock()
	save := now.Sub(p.lastSeenSave) > time.Minute
	if save {
		p.lastSeenSave = now
	}
	p.mu.Unlock()
	if !save {
		return
	}
	e.mu.Lock()
	friend := p.isFriend
	e.mu.Unlock()
	if friend {
		// Runs on the socket read goroutine: never wait for the disk here.
		key, at := p.key.Hex(), now.UTC()
		e.store.UpdateLater(func(d *store.Data) {
			for i := range d.Friends {
				if d.Friends[i].PublicKey == key {
					d.Friends[i].LastSeenAt = at
				}
			}
		})
	}
}

// peerHousekeeping runs once a second per peer.
func (e *Engine) peerHousekeeping(p *peer, now time.Time) {
	online := p.online(now, e.cfg.OfflineAfter)
	p.mu.Lock()
	changed := online != p.wasOnline
	p.wasOnline = online
	helloDue := now.Sub(p.helloSent) >= e.cfg.HelloInterval
	p.mu.Unlock()
	if changed {
		e.log.Info("peer presence changed", "peer", p.displayName(), "online", online)
		e.publishFriends()
	}
	if helloDue {
		e.sendHello(p)
	}
	e.mu.Lock()
	managed := p.managed
	e.mu.Unlock()
	if managed {
		e.maybeSendCandidates(p, now)
	}
}
