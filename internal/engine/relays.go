package engine

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/transport"
	"github.com/NotrinoResearch/lanfolk/internal/version"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// MaxActiveRelays is how many shared relays a client registers at.
const MaxActiveRelays = 3

// relayConn is one shared relay in the effective list.
type relayConn struct {
	cfg    store.Relay
	source string // "default", "network" or "custom"
	client *transport.RelayClient
	cancel context.CancelFunc
}

func (r *relayConn) name() string {
	return net.JoinHostPort(r.cfg.Host, strconv.Itoa(r.cfg.UDPPort))
}

func (r *relayConn) kind() pathmgr.Kind {
	switch r.client.Mode() {
	case transport.ModeUDP:
		return pathmgr.SharedRelay
	case transport.ModeTCP:
		return pathmgr.SharedRelayTCP
	}
	return 0
}

func (r *relayConn) stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

func relayKey(r store.Relay) string {
	return strings.ToLower(r.Host) + "|" + strconv.Itoa(r.UDPPort)
}

// effectiveRelaysLocked returns the relay list in priority order: the build
// default, the active network owner's relays, then the user's relays
// (docs/DECISIONS.md D-015).
func (e *Engine) effectiveRelaysLocked() []relayConn {
	var out []relayConn
	seen := map[string]bool{}
	add := func(r store.Relay, src string) {
		if r.Host == "" || r.UDPPort <= 0 || len(out) >= MaxActiveRelays || seen[relayKey(r)] {
			return
		}
		seen[relayKey(r)] = true
		out = append(out, relayConn{cfg: r, source: src})
	}
	if e.cfg.DefaultRelays != nil {
		for _, r := range e.cfg.DefaultRelays {
			add(r, "default")
		}
	} else if host, udp, tcp, ok := version.DefaultRelay(); ok {
		add(store.Relay{Host: host, UDPPort: udp, TCPPort: tcp}, "default")
	}
	if e.active != nil {
		for _, r := range e.active.Relays {
			add(store.Relay{Host: r.Host, UDPPort: int(r.UDPPort), TCPPort: int(r.TCPPort)}, "network")
		}
	}
	for _, r := range e.settings.CustomRelays {
		add(r, "custom")
	}
	return out
}

// applyRelaysLocked starts and stops relay clients so that they match the
// effective list. Peers are registered at every relay.
func (e *Engine) applyRelaysLocked() {
	if e.ctx == nil {
		return
	}
	want := e.effectiveRelaysLocked()
	old := make(map[string]*relayConn, len(e.relays))
	for _, r := range e.relays {
		old[relayKey(r.cfg)] = r
	}
	next := make([]*relayConn, 0, len(want))
	changed := len(want) != len(e.relays)
	for i, w := range want {
		k := relayKey(w.cfg)
		if r := old[k]; r != nil && r.cfg == w.cfg {
			r.source = w.source
			next = append(next, r)
			delete(old, k)
			if i >= len(e.relays) || e.relays[i] != r {
				changed = true
			}
			continue
		}
		changed = true
		next = append(next, e.startRelay(w))
	}
	for _, r := range old {
		r.stop()
	}
	if !changed {
		return
	}
	e.relays = next
	e.relayIx.Store(&next)
	for _, p := range e.peers {
		for _, r := range next {
			r.client.Register(p.sid)
		}
		if p.managed {
			p.mu.Lock()
			if p.pm != nil {
				// Relay indexes moved: rebuild the shared relay paths.
				for i := 0; i < MaxActiveRelays; i++ {
					p.pm.SetSharedRelay(uint8(i), 0, false, nanotime())
				}
			}
			p.mu.Unlock()
		}
	}
	e.log.Info("relay list applied", "relays", len(next))
}

func (e *Engine) startRelay(w relayConn) *relayConn {
	r := &relayConn{cfg: w.cfg, source: w.source}
	opts := transport.RelayOptions{
		Host: w.cfg.Host, UDPPort: w.cfg.UDPPort, TCPPort: w.cfg.TCPPort, Logger: e.log.With("relay", r.name()),
		OnData: func(sid wire.SessionID, payload []byte, viaTCP bool) {
			e.onRelayPayload(r, sid, payload, viaTCP)
		},
		OnPeerJoined: func(sid wire.SessionID) {
			if p := e.peerBySID(sid); p != nil {
				go e.sendHello(p)
			} else if is := e.inviteSession(sid); is != nil && !is.owner {
				go e.sendJoinRequest(is, true)
			}
		},
		OnObserved:   func(netip.AddrPort) { go e.refreshCandidates(true) },
		OnModeChange: func(transport.Mode) { go e.publishStatus() },
	}
	if e.cfg.RelayOptions != nil {
		e.cfg.RelayOptions(&opts)
	}
	r.client = transport.NewRelayClient(e.sock, opts)
	ctx, cancel := context.WithCancel(e.ctx)
	r.cancel = cancel
	for _, p := range e.peers {
		r.client.Register(p.sid)
	}
	for sid := range e.invSess {
		r.client.Register(sid)
	}
	e.wg.Add(1)
	go func() { defer e.wg.Done(); r.client.Run(ctx) }()
	return r
}

// onRelayPayload handles data a shared relay delivered for a session.
func (e *Engine) onRelayPayload(r *relayConn, sid wire.SessionID, payload []byte, viaTCP bool) {
	p := e.peerBySID(sid)
	if p == nil {
		// Not a friend or member: it may belong to an invite session.
		if is := e.inviteSession(sid); is != nil && wire.Classify(payload) == wire.ClassControl {
			if s, ok := wire.ControlSession(payload); ok && s == sid {
				e.onInviteFrame(r, is, payload)
			}
		}
		return
	}
	idx := e.relayIndex(r)
	if idx < 0 {
		return
	}
	kind := pathmgr.SharedRelay
	if viaTCP {
		kind = pathmgr.SharedRelayTCP
	}
	switch wire.Classify(payload) {
	case wire.ClassWireGuard:
		p.noteInbound(e.relayPathID(p, idx))
		e.bind.Deliver(payload, p.ep)
	case wire.ClassControl:
		if s, ok := wire.ControlSession(payload); ok && s == sid {
			e.onControlFrom(p, payload, via{kind: kind, relay: idx})
		}
	}
}

func (e *Engine) relayIndex(r *relayConn) int {
	rs := e.relayIx.Load()
	if rs == nil {
		return -1
	}
	for i, x := range *rs {
		if x == r {
			return i
		}
	}
	return -1
}

func (e *Engine) relayPathID(p *peer, idx int) pathmgr.PathID {
	if v := p.active.Load(); v != nil && v.kind.IsRelay() && v.kind != pathmgr.MemberRelay && v.relay == idx {
		return v.pathID
	}
	return 0
}

// relaysForNetwork converts owner relays for the API.
func relaysOf(l *network.List) []store.Relay {
	var out []store.Relay
	for _, r := range l.Relays {
		out = append(out, store.Relay{Host: r.Host, UDPPort: int(r.UDPPort), TCPPort: int(r.TCPPort)})
	}
	return out
}
