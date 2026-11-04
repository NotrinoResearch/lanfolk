// Package pathmgr selects the path used to reach one peer. It probes
// candidate paths, measures round trips, prefers direct paths, fails over
// when the active path goes silent and reports live quality statistics.
//
// The Manager is a pure state machine: callers pass monotonic nanosecond
// timestamps (internal/clock) and send the probes Tick returns. It is not
// safe for concurrent use.
package pathmgr

import (
	"crypto/rand"
	"encoding/binary"
	"net/netip"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

// Kind is the type of a path.
type Kind uint8

const (
	Direct Kind = iota + 1
	MemberRelay
	SharedRelay
	SharedRelayTCP
)

func (k Kind) String() string {
	switch k {
	case Direct:
		return "direct"
	case MemberRelay:
		return "member-relay"
	case SharedRelay:
		return "shared-relay"
	case SharedRelayTCP:
		return "tcp-fallback"
	}
	return "none"
}

// IsRelay reports whether traffic on the path passes a third party.
func (k Kind) IsRelay() bool { return k != Direct }

// Source tells where a direct candidate came from.
type Source uint8

const (
	SourceLocal Source = iota + 1
	SourceIPv6
	SourceSTUN
	SourcePortMap
	SourceManual
	SourcePeerReflexive
	SourceCache
	SourceConnCode
	SourceDHT
	SourceRelayed // learned through another member
)

func (s Source) String() string {
	return [...]string{"", "local", "ipv6", "stun", "port-mapping", "manual", "peer-reflexive", "cache",
		"connection-code", "dht", "member"}[s]
}

// PathID identifies a path within one Manager.
type PathID uint32

// Quality is the user-facing connection grade.
type Quality uint8

const (
	QualityUnknown Quality = iota
	Good
	Fair
	Poor
)

func (q Quality) String() string { return [...]string{"unknown", "good", "fair", "poor"}[q] }

// Thresholds grade a connection. Good needs all three within the good
// limits; Poor is any value beyond the poor limits; the rest is Fair.
type Thresholds struct {
	GoodRTT    time.Duration `json:"good_rtt"`
	GoodJitter time.Duration `json:"good_jitter"`
	GoodLoss   float64       `json:"good_loss"`
	PoorRTT    time.Duration `json:"poor_rtt"`
	PoorJitter time.Duration `json:"poor_jitter"`
	PoorLoss   float64       `json:"poor_loss"`
}

// DefaultThresholds are the plan's initial values.
func DefaultThresholds() Thresholds {
	return Thresholds{
		GoodRTT: 60 * time.Millisecond, GoodJitter: 10 * time.Millisecond, GoodLoss: 0.005,
		PoorRTT: 120 * time.Millisecond, PoorJitter: 30 * time.Millisecond, PoorLoss: 0.02,
	}
}

// Grade applies the thresholds.
func (t Thresholds) Grade(rtt, jitter time.Duration, loss float64) Quality {
	if rtt > t.PoorRTT || jitter > t.PoorJitter || loss > t.PoorLoss {
		return Poor
	}
	if rtt <= t.GoodRTT && jitter <= t.GoodJitter && loss <= t.GoodLoss {
		return Good
	}
	return Fair
}

// Config holds timing and selection parameters.
type Config struct {
	BurstInterval    time.Duration // probe interval while hole punching
	BurstDuration    time.Duration // length of a probe burst
	ActiveInterval   time.Duration // probe interval on the active path
	StandbyInterval  time.Duration // probe interval on working fallback paths
	DeadAfter        time.Duration // silence that fails the active path
	StandbyDeadAfter time.Duration // silence that fails a standby path
	RetryMin         time.Duration // first retry delay of a failed path
	RetryMax         time.Duration // maximum retry delay
	DirectPreference time.Duration // relay must be this much faster to beat direct
	TCPPenalty       time.Duration // added to the TCP fallback RTT
	Hysteresis       time.Duration // gain needed to switch between similar paths
	StatsWindow      time.Duration
	OfferTTL         time.Duration // member relay offers expire after this
	Thresholds       Thresholds
	RelayOnly        bool // never use or probe direct paths
}

// DefaultConfig returns the plan's values: probes every 250 ms for 3 s,
// direct preferred unless a relay is 5 ms better, failover after 2 s of
// silence, keepalives far below the 15 s limit.
func DefaultConfig() Config {
	return Config{
		BurstInterval:    250 * time.Millisecond,
		BurstDuration:    3 * time.Second,
		ActiveInterval:   500 * time.Millisecond,
		StandbyInterval:  5 * time.Second,
		DeadAfter:        2 * time.Second,
		StandbyDeadAfter: 16 * time.Second,
		RetryMin:         30 * time.Second,
		RetryMax:         5 * time.Minute,
		DirectPreference: 5 * time.Millisecond,
		TCPPenalty:       10 * time.Millisecond,
		Hysteresis:       2 * time.Millisecond,
		StatsWindow:      60 * time.Second,
		OfferTTL:         20 * time.Second,
		Thresholds:       DefaultThresholds(),
	}
}

// MaxDirectPaths bounds the number of direct candidates per peer.
const MaxDirectPaths = 24

type path struct {
	id         PathID
	kind       Kind
	addr       netip.AddrPort
	via        identity.PublicKey
	source     Source
	added      int64
	burstUntil int64
	nextProbe  int64
	retryAt    int64
	backoff    int64
	lastRecv   int64
	srtt       int64
	lastRTT    int64
	wasAlive   bool
	everAlive  bool
	relay      uint8 // shared relay: index in the effective relay list
	present    bool  // shared relay: the peer is registered too
	advertised int64 // member relay: relaying member's RTT to the peer
	offerAt    int64
}

// PathInfo is a read-only view of a path.
type PathInfo struct {
	ID         PathID
	Kind       Kind
	Addr       netip.AddrPort
	Via        identity.PublicKey
	Source     Source
	Relay      uint8 // shared relay index
	Alive      bool
	Active     bool
	SRTT       time.Duration
	LastRTT    time.Duration
	Advertised time.Duration
}

// Probe asks the caller to send a PathProbe with ID on Path.
type Probe struct {
	Path PathID
	ID   uint64
}

const ringSize = 1024

type probeRec struct {
	id    uint64
	path  PathID
	sent  int64
	acked int64
}

// Manager tracks the paths to one peer.
type Manager struct {
	cfg       Config
	paths     map[PathID]*path
	order     []PathID
	nextID    PathID
	active    *path
	activeVer uint64
	ring      [ringSize]probeRec
	nextProbe uint64
	firstID   uint64
}

// New creates a Manager.
func New(cfg Config) *Manager {
	var b [8]byte
	rand.Read(b[:])
	start := binary.BigEndian.Uint64(b[:]) >> 1
	return &Manager{cfg: cfg, paths: make(map[PathID]*path), nextProbe: start, firstID: start}
}

// SetConfig replaces the configuration (for example when Relay only changes).
func (m *Manager) SetConfig(cfg Config) { m.cfg = cfg }

func (m *Manager) add(p *path, now int64) PathID {
	m.nextID++
	p.id = m.nextID
	p.added = now
	m.paths[p.id] = p
	m.order = append(m.order, p.id)
	m.startBurst(p, now)
	return p.id
}

func (m *Manager) remove(id PathID) {
	p := m.paths[id]
	if p == nil {
		return
	}
	delete(m.paths, id)
	for i, o := range m.order {
		if o == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if m.active == p {
		m.active = nil
		m.activeVer++
	}
}

func (m *Manager) startBurst(p *path, now int64) {
	p.burstUntil = now + int64(m.cfg.BurstDuration)
	p.nextProbe = now
	p.retryAt = 0
}

// AddDirect adds a direct candidate. It returns the path and whether it is
// new. Re-adding a known address keeps the existing path.
func (m *Manager) AddDirect(addr netip.AddrPort, src Source, now int64) (PathID, bool) {
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	for _, id := range m.order {
		if p := m.paths[id]; p.kind == Direct && p.addr == addr {
			return id, false
		}
	}
	direct := 0
	for _, id := range m.order {
		if m.paths[id].kind == Direct {
			direct++
		}
	}
	if direct >= MaxDirectPaths {
		m.pruneDirect()
	}
	return m.add(&path{kind: Direct, addr: addr, source: src}, now), true
}

// pruneDirect drops the least useful direct path: never-working ones first,
// then the oldest.
func (m *Manager) pruneDirect() {
	var victim *path
	for _, id := range m.order {
		p := m.paths[id]
		if p.kind != Direct || p == m.active {
			continue
		}
		if victim == nil || (!p.everAlive && victim.everAlive) ||
			(p.everAlive == victim.everAlive && p.added < victim.added) {
			victim = p
		}
	}
	if victim != nil {
		m.remove(victim.id)
	}
}

// FindDirect returns the direct path for addr.
func (m *Manager) FindDirect(addr netip.AddrPort) (PathID, bool) {
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	for _, id := range m.order {
		if p := m.paths[id]; p.kind == Direct && p.addr == addr {
			return id, true
		}
	}
	return 0, false
}

func isShared(k Kind) bool { return k == SharedRelay || k == SharedRelayTCP }

func (m *Manager) relayPath(relay uint8) *path {
	for _, id := range m.order {
		if p := m.paths[id]; isShared(p.kind) && p.relay == relay {
			return p
		}
	}
	return nil
}

// bootstrapRelay returns the highest-priority shared relay at which the
// peer is present.
func (m *Manager) bootstrapRelay() *path {
	var best *path
	for _, id := range m.order {
		if p := m.paths[id]; isShared(p.kind) && p.present && (best == nil || p.relay < best.relay) {
			best = p
		}
	}
	return best
}

// SetSharedRelay updates the path through shared relay number relay (its
// index in the effective relay list; lower is preferred before RTTs are
// known). kind is SharedRelay, SharedRelayTCP, or 0 when that relay is
// unreachable. present reports whether the peer is registered there.
func (m *Manager) SetSharedRelay(relay uint8, kind Kind, present bool, now int64) PathID {
	p := m.relayPath(relay)
	if kind == 0 {
		if p != nil {
			m.remove(p.id)
		}
		return 0
	}
	if p == nil {
		return m.add(&path{kind: kind, relay: relay, present: present}, now)
	}
	if p.kind != kind {
		// Transport changed; old measurements do not apply.
		p.kind = kind
		p.srtt, p.lastRTT, p.lastRecv, p.wasAlive = 0, 0, 0, false
		m.startBurst(p, now)
	}
	if present && !p.present {
		m.startBurst(p, now)
	}
	p.present = present
	return p.id
}

// SetMemberRelay adds or refreshes a path through another member that has a
// direct path to the peer. viaAddr is the relaying member's address.
func (m *Manager) SetMemberRelay(via identity.PublicKey, viaAddr netip.AddrPort, advertised time.Duration, now int64) PathID {
	for _, id := range m.order {
		if p := m.paths[id]; p.kind == MemberRelay && p.via == via {
			p.addr = viaAddr
			p.advertised = int64(advertised)
			p.offerAt = now
			return id
		}
	}
	return m.add(&path{kind: MemberRelay, via: via, addr: viaAddr, advertised: int64(advertised), offerAt: now}, now)
}

// RemoveMemberRelay removes the path through via.
func (m *Manager) RemoveMemberRelay(via identity.PublicKey) {
	for _, id := range append([]PathID(nil), m.order...) {
		if p := m.paths[id]; p.kind == MemberRelay && p.via == via {
			m.remove(id)
		}
	}
}

// Restart begins a new probe burst on every path, for example after a
// local network change.
func (m *Manager) Restart(now int64) {
	for _, p := range m.paths {
		p.backoff = 0
		m.startBurst(p, now)
	}
}

func (m *Manager) deadAfter(p *path) int64 {
	if p == m.active {
		return int64(m.cfg.DeadAfter)
	}
	return int64(m.cfg.StandbyDeadAfter)
}

func (m *Manager) alive(p *path, now int64) bool {
	if isShared(p.kind) && !p.present {
		return false
	}
	return p.lastRecv != 0 && now-p.lastRecv < m.deadAfter(p)
}

func (m *Manager) probeable(p *path) bool {
	if m.cfg.RelayOnly && p.kind == Direct {
		return false
	}
	if isShared(p.kind) && !p.present {
		return false
	}
	return true
}

func (m *Manager) nextBackoff(p *path) int64 {
	if p.backoff == 0 {
		p.backoff = int64(m.cfg.RetryMin)
	} else {
		p.backoff *= 2
		if p.backoff > int64(m.cfg.RetryMax) {
			p.backoff = int64(m.cfg.RetryMax)
		}
	}
	return p.backoff
}

// Tick advances timers, returns the probes to send now and updates the
// active path.
func (m *Manager) Tick(now int64) []Probe {
	if ttl := int64(m.cfg.OfferTTL); ttl > 0 {
		for _, id := range append([]PathID(nil), m.order...) {
			if p := m.paths[id]; p.kind == MemberRelay && now-p.offerAt > ttl {
				m.remove(id)
			}
		}
	}
	var probes []Probe
	for _, id := range m.order {
		p := m.paths[id]
		if !m.probeable(p) {
			continue
		}
		alive := m.alive(p, now)
		if alive {
			p.wasAlive, p.everAlive, p.backoff = true, true, 0
		} else if p.wasAlive {
			p.wasAlive = false
			m.startBurst(p, now) // just failed: try to recover quickly
		}
		if !alive && now >= p.burstUntil {
			if p.retryAt == 0 {
				p.retryAt = now + m.nextBackoff(p)
			}
			if now < p.retryAt {
				continue
			}
			m.startBurst(p, now)
		}
		if now >= p.nextProbe {
			probes = append(probes, m.record(p, now))
			p.nextProbe = now + m.interval(p, now)
		}
	}
	m.selectPath(now)
	return probes
}

func (m *Manager) interval(p *path, now int64) int64 {
	switch {
	case now < p.burstUntil:
		return int64(m.cfg.BurstInterval)
	case p == m.active:
		return int64(m.cfg.ActiveInterval)
	default:
		return int64(m.cfg.StandbyInterval)
	}
}

func (m *Manager) record(p *path, now int64) Probe {
	id := m.nextProbe
	m.nextProbe++
	m.ring[id%ringSize] = probeRec{id: id, path: p.id, sent: now}
	return Probe{Path: p.id, ID: id}
}

// OnAck records a PathProbeAck. It returns the path the probe was sent on.
func (m *Manager) OnAck(id uint64, now int64) (PathID, bool) {
	rec := &m.ring[id%ringSize]
	if rec.id != id || rec.acked != 0 || rec.sent == 0 {
		return 0, false
	}
	rec.acked = now
	p := m.paths[rec.path]
	if p == nil {
		return 0, false
	}
	rtt := now - rec.sent
	if rtt <= 0 {
		rtt = 1
	}
	p.lastRTT = rtt
	if p.srtt == 0 {
		p.srtt = rtt
	} else {
		p.srtt += (rtt - p.srtt) / 4
	}
	p.lastRecv = now
	return p.id, true
}

// OnInbound records any authenticated packet received on a path.
func (m *Manager) OnInbound(id PathID, now int64) {
	if p := m.paths[id]; p != nil && p.srtt > 0 {
		p.lastRecv = now
	}
}

func (m *Manager) usable(p *path, now int64) bool {
	return m.probeable(p) && m.alive(p, now) && p.srtt > 0
}

func (m *Manager) effective(p *path) int64 {
	if p.kind == SharedRelayTCP {
		return p.srtt + int64(m.cfg.TCPPenalty)
	}
	return p.srtt
}

func (m *Manager) selectPath(now int64) {
	var bestDirect, bestRelay *path
	for _, id := range m.order {
		p := m.paths[id]
		if !m.usable(p, now) {
			continue
		}
		if p.kind == Direct {
			if bestDirect == nil || m.effective(p) < m.effective(bestDirect) {
				bestDirect = p
			}
		} else if bestRelay == nil || m.effective(p) < m.effective(bestRelay) {
			bestRelay = p
		}
	}
	var choice *path
	switch {
	case bestDirect != nil && (bestRelay == nil || m.effective(bestRelay)+int64(m.cfg.DirectPreference) > m.effective(bestDirect)):
		choice = bestDirect
	case bestRelay != nil:
		choice = bestRelay
	default:
		// Nothing measured yet: start on the shared relay if the peer is
		// registered there, so traffic flows while probes run.
		if rp := m.bootstrapRelay(); rp != nil {
			choice = rp
		}
	}
	if choice == m.active {
		return
	}
	if cur := m.active; cur != nil && choice != nil && m.usable(cur, now) &&
		cur.kind.IsRelay() == choice.kind.IsRelay() &&
		m.effective(choice)+int64(m.cfg.Hysteresis) >= m.effective(cur) {
		return
	}
	if choice == nil && m.active != nil && m.paths[m.active.id] != nil {
		// Keep sending on the last path; something may still get through.
		return
	}
	m.active = choice
	m.activeVer++
	if choice != nil {
		// The new active path is probed at the active rate right away.
		if choice.nextProbe > now+int64(m.cfg.ActiveInterval) {
			choice.nextProbe = now
		}
	}
}

func (m *Manager) info(p *path, now int64) PathInfo {
	return PathInfo{
		ID: p.id, Kind: p.kind, Addr: p.addr, Via: p.via, Source: p.source, Relay: p.relay,
		Alive: m.alive(p, now), Active: p == m.active,
		SRTT: time.Duration(p.srtt), LastRTT: time.Duration(p.lastRTT), Advertised: time.Duration(p.advertised),
	}
}

// Active returns the active path.
func (m *Manager) Active() (PathInfo, bool) {
	if m.active == nil {
		return PathInfo{}, false
	}
	return m.info(m.active, 0), true
}

// ActiveVersion changes whenever the active path changes.
func (m *Manager) ActiveVersion() uint64 { return m.activeVer }

// Path returns one path.
func (m *Manager) Path(id PathID) (PathInfo, bool) {
	p := m.paths[id]
	if p == nil {
		return PathInfo{}, false
	}
	return m.info(p, 0), true
}

// Paths lists all paths.
func (m *Manager) Paths(now int64) []PathInfo {
	out := make([]PathInfo, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.info(m.paths[id], now))
	}
	return out
}

// Stats describes the active path over the statistics window.
type Stats struct {
	Kind      Kind
	Relay     uint8
	Via       identity.PublicKey
	Addr      netip.AddrPort
	LatestRTT time.Duration
	AvgRTT    time.Duration
	Jitter    time.Duration
	Loss      float64
	Samples   int
	Quality   Quality
}

// Stats computes live statistics for the active path.
func (m *Manager) Stats(now int64) Stats {
	p := m.active
	if p == nil {
		return Stats{}
	}
	st := Stats{Kind: p.kind, Relay: p.relay, Via: p.via, Addr: p.addr, LatestRTT: time.Duration(p.lastRTT)}
	from := now - int64(m.cfg.StatsWindow)
	grace := now - int64(time.Second)
	var sum, jitterSum int64
	var prev int64 = -1
	var jitterN, sent, lost int
	first := m.nextProbe - ringSize
	if m.nextProbe < m.firstID+ringSize {
		first = m.firstID
	}
	for id := first; id < m.nextProbe; id++ {
		r := &m.ring[id%ringSize]
		if r.id != id || r.path != p.id || r.sent < from {
			continue
		}
		if r.acked != 0 {
			rtt := r.acked - r.sent
			sum += rtt
			st.Samples++
			if prev >= 0 {
				d := rtt - prev
				if d < 0 {
					d = -d
				}
				jitterSum += d
				jitterN++
			}
			prev = rtt
		}
		if r.sent <= grace {
			sent++
			if r.acked == 0 {
				lost++
			}
		}
	}
	if st.Samples == 0 {
		return st
	}
	st.AvgRTT = time.Duration(sum / int64(st.Samples))
	if jitterN > 0 {
		st.Jitter = time.Duration(jitterSum / int64(jitterN))
	}
	if sent > 0 {
		st.Loss = float64(lost) / float64(sent)
	}
	st.Quality = m.cfg.Thresholds.Grade(st.AvgRTT, st.Jitter, st.Loss)
	return st
}
