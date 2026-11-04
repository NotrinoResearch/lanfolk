package engine

import (
	"net/netip"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/transport"
	"golang.zx2c4.com/wireguard/conn"
)

// SendWireGuard implements transport.Sender: WireGuard packets go out on
// the peer's active path, chosen at send time.
func (e *Engine) SendWireGuard(pkt []byte, ep conn.Endpoint) error {
	switch ep := ep.(type) {
	case *transport.PeerEndpoint:
		p := e.peerByKey(ep.Key)
		if p == nil {
			return errNoPath
		}
		v := p.active.Load()
		if v == nil {
			v = e.relayVia(p)
		}
		if v == nil {
			return errNoPath
		}
		return e.sendVia(p, pkt, v)
	case *transport.RawEndpoint:
		return e.sock.WriteTo(pkt, ep.Addr)
	}
	return errNoPath
}

// tickPaths advances every managed peer's path manager. It runs every
// TickInterval and takes no engine lock in the common case.
func (e *Engine) tickPaths() {
	now := nanotime()
	var relays []*relayConn
	if rs := e.relayIx.Load(); rs != nil {
		relays = *rs
	}
	addrsChanged := false
	for _, p := range e.managedPeers() {
		p.mu.Lock()
		pm := p.pm
		if pm == nil {
			p.mu.Unlock()
			continue
		}
		for i, r := range relays {
			pm.SetSharedRelay(uint8(i), r.kind(), r.client.PeerPresent(p.sid), now)
		}
		if id := pathmgr.PathID(p.inboundPath.Load()); id != 0 {
			pm.OnInbound(id, p.inboundAt.Load())
		}
		probes := pm.Tick(now)
		type send struct {
			info pathmgr.PathInfo
			id   uint64
		}
		sends := make([]send, 0, len(probes))
		for _, pr := range probes {
			if info, ok := pm.Path(pr.Path); ok {
				sends = append(sends, send{info, pr.ID})
			}
		}
		changed := pm.ActiveVersion() != p.pathVer
		var info pathmgr.PathInfo
		var hasActive bool
		var st pathmgr.Stats
		if changed {
			p.pathVer = pm.ActiveVersion()
			info, hasActive = pm.Active()
			st = pm.Stats(now) // the UI shows the new path's numbers at once
		}
		p.mu.Unlock()
		if changed {
			p.stats.Store(&st)
		}
		// viaForInfo takes e.mu, so it runs after p.mu is released
		// (rebuildAddrMap takes p.mu while holding e.mu).
		for _, s := range sends {
			if v := e.viaForInfo(s.info); v != nil {
				e.sendMsg(p, &peermsg.PathProbe{ProbeID: s.id, PathID: uint32(v.pathID)}, v)
			}
		}
		if changed {
			addrsChanged = true
			if hasActive {
				nv := e.viaForInfo(info)
				p.active.Store(nv)
				e.log.Info("path selected", "peer", p.displayName(), "path", info.Kind.String(), "detail", e.pathDetail(info))
				if info.Kind == pathmgr.Direct {
					e.rememberDirect(p, info.Addr)
				}
			} else {
				p.active.Store(nil)
			}
		}
	}
	if addrsChanged {
		e.mu.Lock()
		e.rebuildAddrMap()
		e.mu.Unlock()
	}
}

// updateStats refreshes the live statistics once a second; only the UI and
// relay offers read them.
func (e *Engine) updateStats() {
	now := nanotime()
	for _, p := range e.managedPeers() {
		p.mu.Lock()
		if p.pm == nil {
			p.mu.Unlock()
			continue
		}
		st := p.pm.Stats(now)
		p.mu.Unlock()
		p.stats.Store(&st)
	}
}

func (e *Engine) viaForInfo(info pathmgr.PathInfo) *via {
	v := &via{kind: info.Kind, addr: info.Addr, relay: int(info.Relay), pathID: info.ID}
	if info.Kind == pathmgr.MemberRelay {
		e.mu.Lock()
		v.relayer = e.peers[info.Via]
		e.mu.Unlock()
		if v.relayer == nil {
			return nil
		}
	}
	return v
}

func (e *Engine) pathDetail(info pathmgr.PathInfo) string {
	switch info.Kind {
	case pathmgr.MemberRelay:
		e.mu.Lock()
		r := e.peers[info.Via]
		e.mu.Unlock()
		if r != nil {
			return r.displayName()
		}
	case pathmgr.SharedRelay, pathmgr.SharedRelayTCP:
		rs := e.relayIx.Load()
		if rs != nil && int(info.Relay) < len(*rs) {
			return (*rs)[info.Relay].name()
		}
	case pathmgr.Direct:
		return info.Source.String()
	}
	return ""
}

// rememberDirect stores a working direct address (peer_cache), at most once
// a minute per peer.
func (e *Engine) rememberDirect(p *peer, a netip.AddrPort) {
	now := time.Now()
	p.mu.Lock()
	if now.Sub(p.cacheAt) < time.Minute {
		p.mu.Unlock()
		return
	}
	p.cacheAt = now
	p.mu.Unlock()
	// Runs on the engine loop: never wait for the disk here.
	key, addr, at := p.key.Hex(), a.String(), now.UTC()
	e.store.UpdateLater(func(d *store.Data) {
		d.RememberPath(key, addr, pathmgr.Direct.String(), at)
	})
}

// maybeSendCandidates sends our candidates when they changed or every minute.
func (e *Engine) maybeSendCandidates(p *peer, now time.Time) {
	e.mu.Lock()
	gen := e.candGen
	cands := append([]candidate(nil), e.cands...)
	relayOnly := e.settings.RelayOnly
	e.mu.Unlock()
	if relayOnly {
		return
	}
	p.mu.Lock()
	due := p.candGen != gen || now.Sub(p.candSentAt) > time.Minute
	if due {
		p.candGen, p.candSentAt = gen, now
	}
	p.mu.Unlock()
	if !due {
		return
	}
	msg := &peermsg.Candidates{Gen: gen}
	for _, c := range cands {
		msg.Addrs = append(msg.Addrs, peermsg.Candidate{Source: uint8(c.source), Addr: c.addr})
	}
	e.sendMsg(p, msg, nil)
}

func (e *Engine) onCandidates(p *peer, m *peermsg.Candidates) {
	if e.relayOnly() {
		return
	}
	now := nanotime()
	added := false
	p.mu.Lock()
	if p.pm != nil {
		for _, c := range m.Addrs {
			if !usableRemote(c.Addr) {
				continue
			}
			src := pathmgr.Source(c.Source)
			if src == 0 || src > pathmgr.SourceRelayed {
				src = pathmgr.SourceSTUN
			}
			if _, fresh := p.pm.AddDirect(c.Addr, src, now); fresh {
				added = true
			}
		}
	}
	p.mu.Unlock()
	if added {
		e.mu.Lock()
		e.rebuildAddrMap()
		e.mu.Unlock()
		// Answer right away so both sides probe at the same time (hole punching).
		p.mu.Lock()
		p.candSentAt = time.Time{}
		p.mu.Unlock()
		e.maybeSendCandidates(p, time.Now())
	}
}

// addCandidates adds candidates learned out of band (connection code, DHT).
func (e *Engine) addCandidates(pk identity.PublicKey, addrs []netip.AddrPort, src pathmgr.Source) bool {
	e.mu.Lock()
	p := e.peers[pk]
	e.mu.Unlock()
	if p == nil {
		return false
	}
	now := nanotime()
	p.mu.Lock()
	if p.pm == nil {
		p.mu.Unlock()
		return false
	}
	for _, a := range addrs {
		if usableRemote(a) {
			p.pm.AddDirect(a, src, now)
		}
	}
	p.mu.Unlock()
	e.mu.Lock()
	e.rebuildAddrMap()
	e.mu.Unlock()
	return true
}

// usableRemote rejects addresses a peer must never make us send to.
func usableRemote(a netip.AddrPort) bool {
	ip := a.Addr().Unmap()
	if !a.IsValid() || a.Port() == 0 || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 10 && b[1] == 77 && b[2] == 0 { // our own virtual network
			return false
		}
		if b == [4]byte{255, 255, 255, 255} {
			return false
		}
	}
	return true
}
