package engine

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/discovery"
	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
)

// dhtFallback runs the Mainline DHT only while no relay is reachable
// (docs/DECISIONS.md D-013): it starts after DHTAfter without any relay,
// publishes our candidates for each unconnected member, looks theirs up and
// shuts down when a relay comes back.
type dhtFallback struct {
	e         *Engine
	mu        sync.Mutex
	downSince time.Time
	dht       *discovery.DHT
	starting  bool
	cancel    context.CancelFunc
	published map[identity.PublicKey]time.Time
	looked    map[identity.PublicKey]time.Time
}

func newDHTFallback(e *Engine) *dhtFallback {
	return &dhtFallback{e: e, published: map[identity.PublicKey]time.Time{}, looked: map[identity.PublicKey]time.Time{}}
}

// State reports "off", "standby" or "running".
func (d *dhtFallback) state(enabled bool) string {
	if d == nil {
		return "off"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !enabled {
		return "off"
	}
	if d.dht != nil {
		return "running"
	}
	return "standby"
}

func (d *dhtFallback) relaysDown() bool {
	rs := d.e.relayIx.Load()
	if rs == nil {
		return true
	}
	for _, r := range *rs {
		if r.kind() != 0 {
			return false
		}
	}
	return true
}

func (d *dhtFallback) tick(now time.Time) {
	e := d.e
	e.mu.Lock()
	enabled := e.settings.DHTFallback && e.active != nil
	e.mu.Unlock()
	down := d.relaysDown()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !enabled || !down {
		d.downSince = time.Time{}
		d.stopLocked()
		return
	}
	if d.downSince.IsZero() {
		d.downSince = now
	}
	if d.dht == nil {
		if !d.starting && now.Sub(d.downSince) >= e.cfg.DHTAfter {
			d.starting = true
			ctx, cancel := context.WithCancel(e.ctx)
			d.cancel = cancel
			go d.start(ctx)
		}
		return
	}
	for _, p := range e.unconnectedMembers() {
		if now.Sub(d.published[p.key]) > 10*time.Minute {
			d.published[p.key] = now
			go d.publish(p)
		}
		if now.Sub(d.looked[p.key]) > 30*time.Second {
			d.looked[p.key] = now
			go d.lookup(p)
		}
	}
}

func (d *dhtFallback) start(ctx context.Context) {
	e := d.e
	e.log.Info("no relay reachable; starting DHT fallback")
	dht, err := discovery.StartDHT(ctx, discovery.DHTOptions{Bootstrap: e.cfg.DHTBootstrap,
		NoBootstrapDefaults: len(e.cfg.DHTBootstrap) > 0, Logger: e.log.With("component", "dht")})
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starting = false
	if err != nil {
		e.log.Warn("DHT fallback failed to start", "err", err)
		return
	}
	if ctx.Err() != nil {
		dht.Close()
		return
	}
	d.dht = dht
	go e.publishStatus()
}

func (d *dhtFallback) stopLocked() {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.dht != nil {
		d.dht.Close()
		d.dht = nil
		d.published = map[identity.PublicKey]time.Time{}
		d.looked = map[identity.PublicKey]time.Time{}
		d.e.log.Info("relay reachable again; DHT fallback stopped")
		go d.e.publishStatus()
	}
}

func (d *dhtFallback) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopLocked()
}

func (d *dhtFallback) current() *discovery.DHT {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dht
}

func (d *dhtFallback) publish(p *peer) {
	dht := d.current()
	if dht == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.e.ctx, 60*time.Second)
	defer cancel()
	rec := discovery.Record{Published: time.Now(), Candidates: d.e.candidateAddrs()}
	if err := dht.Publish(ctx, p.pair, rec); err != nil {
		d.e.log.Debug("DHT publish failed", "peer", p.displayName(), "err", err)
	}
}

func (d *dhtFallback) lookup(p *peer) {
	dht := d.current()
	if dht == nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.e.ctx, 45*time.Second)
	defer cancel()
	rec, err := dht.Lookup(ctx, p.pair, 2*time.Hour)
	if err != nil {
		return
	}
	if d.e.addCandidates(p.key, rec.Candidates, pathmgr.SourceDHT) {
		d.e.log.Info("found peer candidates in the DHT", "peer", p.displayName(), "candidates", len(rec.Candidates))
	}
}

// unconnectedMembers lists managed peers without any working path.
func (e *Engine) unconnectedMembers() []*peer {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*peer
	for _, p := range e.peers {
		if p.managed && p.active.Load() == nil {
			out = append(out, p)
		}
	}
	return out
}

func (e *Engine) candidateAddrs() []netip.AddrPort {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.settings.RelayOnly {
		return nil
	}
	out := make([]netip.AddrPort, 0, len(e.cands))
	for _, c := range e.cands {
		out = append(out, c.addr)
	}
	return out
}

// ConnCodeTTL is how long a connection code is valid.
const ConnCodeTTL = 10 * time.Minute

// CreateConnCode returns a time-limited code with our candidates for one
// friend or member to paste when nothing else works.
func (e *Engine) CreateConnCode(pk identity.PublicKey) (string, time.Time, error) {
	e.mu.Lock()
	p := e.peers[pk]
	e.mu.Unlock()
	if p == nil {
		return "", time.Time{}, errors.New("unknown friend or member")
	}
	cands := e.candidateAddrs()
	if len(cands) == 0 {
		return "", time.Time{}, errors.New("no addresses to share (Relay only is on, or no network is available)")
	}
	now := time.Now()
	code, err := discovery.CreateConnCode(p.pair, cands, ConnCodeTTL, now)
	return code, now.Add(ConnCodeTTL), err
}

// UseConnCode reads a connection code from a friend or member and starts
// probing the addresses in it.
func (e *Engine) UseConnCode(code string) (identity.PublicKey, error) {
	lookup := func(id identity.KeyID) *identity.Pair {
		if p := e.peerByKeyID(id); p != nil {
			return p.pair
		}
		return nil
	}
	sender, cands, _, err := discovery.ParseConnCode(code, lookup, time.Now())
	if err != nil {
		return identity.PublicKey{}, err
	}
	if !e.addCandidates(sender, cands, pathmgr.SourceConnCode) {
		return sender, errors.New("the code's sender is not a member of the active network")
	}
	return sender, nil
}
