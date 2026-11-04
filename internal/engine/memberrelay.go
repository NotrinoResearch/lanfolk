package engine

import (
	"net/netip"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Member relay limits (docs/PROTOCOL.md, "Member relay").
const (
	memberRelayKbps     = 2000
	relayOfferInterval  = 5 * time.Second
	relayOfferFreshness = 20 * time.Second
)

// directActive returns the peer's active direct path address, if any.
func directActive(p *peer) (netip.AddrPort, time.Duration, bool) {
	v := p.active.Load()
	if v == nil || v.kind != pathmgr.Direct {
		return netip.AddrPort{}, 0, false
	}
	var rtt time.Duration
	if st := p.stats.Load(); st != nil {
		rtt = st.AvgRTT
	}
	return v.addr, rtt, true
}

// onMemberForward handles 0xF3: a member asks us to relay to another member.
func (e *Engine) onMemberForward(b []byte, from netip.AddrPort) {
	sid, target, payload, ok := wire.ParseMemberFrame(b)
	if !ok || e.relayOnly() {
		return
	}
	src := e.peerBySID(sid)
	dst := e.peerByKeyID(target)
	if src == nil || dst == nil || src == dst {
		return
	}
	e.mu.Lock()
	allowed := src.managed && dst.managed
	e.mu.Unlock()
	if !allowed {
		return // only between members of our active network
	}
	// The sender must reach us directly from this address.
	if a, _, ok := directActive(src); !ok || a != from {
		if id := e.directPathID(src, from); id == 0 {
			return
		}
	}
	dstAddr, _, ok := directActive(dst)
	if !ok {
		return
	}
	if !e.relayLimiter.allow(src.keyID, len(payload)) {
		return
	}
	bp := memberFramePool.Get().(*[]byte)
	f := wire.AppendMemberDeliver((*bp)[:0], dst.sid, src.keyID, payload)
	e.sock.WriteTo(f, dstAddr)
	*bp = f
	memberFramePool.Put(bp)
}

// onMemberDeliver handles 0xF4: a relaying member delivers a frame from
// another member.
func (e *Engine) onMemberDeliver(b []byte, from netip.AddrPort) {
	sid, source, payload, ok := wire.ParseMemberFrame(b)
	if !ok {
		return
	}
	relayer := e.peerBySID(sid)
	origin := e.peerByKeyID(source)
	if relayer == nil || origin == nil || relayer == origin {
		return
	}
	if e.directPathID(relayer, from) == 0 {
		return
	}
	v := via{kind: pathmgr.MemberRelay, addr: from, relayer: relayer}
	if a := origin.active.Load(); a != nil && a.kind == pathmgr.MemberRelay && a.relayer == relayer {
		v.pathID = a.pathID // common case: no lock
	} else {
		origin.mu.Lock()
		if origin.pm != nil {
			v.pathID, _ = origin.pm.FindMemberRelay(relayer.key)
		}
		origin.mu.Unlock()
	}
	switch wire.Classify(payload) {
	case wire.ClassWireGuard:
		origin.noteInbound(v.pathID)
		e.bind.Deliver(payload, origin.ep)
	case wire.ClassControl:
		if s, ok := wire.ControlSession(payload); ok && s == origin.sid {
			e.onControlFrom(origin, payload, v)
		}
	}
}

// sendRelayOffers tells each directly connected member which other members
// we reach directly, so that members without a direct path can use us.
func (e *Engine) sendRelayOffers(now time.Time) {
	if e.relayOnly() {
		return
	}
	e.mu.Lock()
	if now.Sub(e.lastOffer) < relayOfferInterval {
		e.mu.Unlock()
		return
	}
	e.lastOffer = now
	var direct []*peer
	for _, p := range e.peers {
		if p.managed {
			if _, _, ok := directActive(p); ok {
				direct = append(direct, p)
			}
		}
	}
	e.mu.Unlock()
	if len(direct) < 2 {
		return
	}
	for _, to := range direct {
		msg := &peermsg.RelayOffer{}
		for _, p := range direct {
			if p == to || len(msg.Entries) >= peermsg.MaxOfferEntries {
				continue
			}
			_, rtt, _ := directActive(p)
			msg.Entries = append(msg.Entries, peermsg.OfferEntry{Peer: p.keyID, RTT: rtt})
		}
		e.sendMsg(to, msg, nil)
	}
}

// onRelayOffer adds member relay paths through the offering member.
func (e *Engine) onRelayOffer(relayer *peer, m *peermsg.RelayOffer) {
	viaAddr, _, ok := directActive(relayer)
	if !ok || e.relayOnly() {
		return
	}
	now := nanotime()
	for _, entry := range m.Entries {
		target := e.peerByKeyID(entry.Peer)
		if target == nil || target == relayer || target.key == e.key.Public() {
			continue
		}
		e.mu.Lock()
		managed := target.managed
		e.mu.Unlock()
		if !managed {
			continue
		}
		target.mu.Lock()
		if target.pm != nil {
			target.pm.SetMemberRelay(relayer.key, viaAddr, entry.RTT, now)
		}
		target.mu.Unlock()
	}
}

// relayLimiter is a per-source token bucket for member relaying.
type relayLimiter struct {
	mu      sync.Mutex
	buckets map[identity.KeyID]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (l *relayLimiter) allow(id identity.KeyID, n int) bool {
	const rate = memberRelayKbps * 1000 / 8
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[identity.KeyID]*bucket)
	}
	b := l.buckets[id]
	if b == nil {
		b = &bucket{tokens: rate, last: now}
		l.buckets[id] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rate
	if b.tokens > rate {
		b.tokens = rate
	}
	b.last = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}
