package pathmgr

import (
	"math"
	"net/netip"
	"sort"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

const ms = int64(time.Millisecond)

// sim drives a Manager with a simulated network: each path has an RTT, a
// loss pattern and an up/down switch.
type sim struct {
	t       *testing.T
	m       *Manager
	now     int64
	rtt     map[PathID]time.Duration
	down    map[PathID]bool
	lossMod map[PathID]int // drop every n-th probe
	pending []ack
	sent    map[PathID]int
	counter map[PathID]int
}

type ack struct {
	at int64
	id uint64
}

func newSim(t *testing.T, cfg Config) *sim {
	return &sim{t: t, m: New(cfg), now: 1_000 * ms, rtt: map[PathID]time.Duration{},
		down: map[PathID]bool{}, lossMod: map[PathID]int{}, sent: map[PathID]int{}, counter: map[PathID]int{}}
}

func (s *sim) run(d time.Duration) {
	end := s.now + int64(d)
	for s.now < end {
		s.now += 10 * ms
		sort.Slice(s.pending, func(i, j int) bool { return s.pending[i].at < s.pending[j].at })
		for len(s.pending) > 0 && s.pending[0].at <= s.now {
			s.m.OnAck(s.pending[0].id, s.pending[0].at)
			s.pending = s.pending[1:]
		}
		for _, p := range s.m.Tick(s.now) {
			s.sent[p.Path]++
			s.counter[p.Path]++
			if s.down[p.Path] {
				continue
			}
			if n := s.lossMod[p.Path]; n > 0 && s.counter[p.Path]%n == 0 {
				continue
			}
			r, ok := s.rtt[p.Path]
			if !ok {
				continue
			}
			s.pending = append(s.pending, ack{at: s.now + int64(r), id: p.ID})
		}
	}
}

func (s *sim) active() PathInfo {
	a, ok := s.m.Active()
	if !ok {
		s.t.Fatal("no active path")
	}
	return a
}

var (
	addrA = netip.MustParseAddrPort("198.51.100.1:4000")
	addrB = netip.MustParseAddrPort("192.168.1.20:4000")
)

func TestBurstThenBackoff(t *testing.T) {
	s := newSim(t, DefaultConfig())
	id, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.run(3500 * time.Millisecond)
	if n := s.sent[id]; n < 12 || n > 14 {
		t.Fatalf("burst sent %d probes, want about 12 (250 ms for 3 s)", n)
	}
	before := s.sent[id]
	s.run(20 * time.Second)
	if s.sent[id] != before {
		t.Fatalf("dead path probed during backoff: %d", s.sent[id]-before)
	}
	s.run(15 * time.Second)
	if s.sent[id] == before {
		t.Fatal("dead path never retried")
	}
}

func TestStartsOnSharedRelayBeforeMeasurements(t *testing.T) {
	s := newSim(t, DefaultConfig())
	s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	s.run(10 * time.Millisecond)
	if a := s.active(); a.Kind != SharedRelay {
		t.Fatalf("active %v", a.Kind)
	}
}

func TestPreferDirectUnlessRelayFiveMsBetter(t *testing.T) {
	cases := []struct {
		direct, relay time.Duration
		want          Kind
	}{
		{20 * time.Millisecond, 40 * time.Millisecond, Direct},
		{20 * time.Millisecond, 16 * time.Millisecond, Direct},
		{20 * time.Millisecond, 14 * time.Millisecond, SharedRelay},
	}
	for _, c := range cases {
		s := newSim(t, DefaultConfig())
		r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
		d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
		s.rtt[r], s.rtt[d] = c.relay, c.direct
		s.run(4 * time.Second)
		if a := s.active(); a.Kind != c.want {
			t.Errorf("direct %v relay %v: active %v, want %v", c.direct, c.relay, a.Kind, c.want)
		}
	}
}

func TestFailoverWithinTwoSecondsAndRecovery(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 60*time.Millisecond, 20*time.Millisecond
	s.run(5 * time.Second)
	if s.active().Kind != Direct {
		t.Fatal("expected direct")
	}
	s.down[d] = true
	cut := s.now
	for s.active().Kind == Direct {
		s.run(10 * time.Millisecond)
		if time.Duration(s.now-cut) > 3*time.Second {
			t.Fatal("no failover within 3 s")
		}
	}
	if gap := time.Duration(s.now - cut); gap > 2600*time.Millisecond {
		t.Fatalf("failover took %v", gap)
	}
	if s.active().Kind != SharedRelay {
		t.Fatalf("fell back to %v", s.active().Kind)
	}
	// The direct path comes back; the manager retries and returns to it.
	s.down[d] = false
	s.run(40 * time.Second)
	if s.active().Kind != Direct {
		t.Fatalf("did not return to direct, active %v", s.active().Kind)
	}
}

func TestNetworkChangeRestartsProbing(t *testing.T) {
	s := newSim(t, DefaultConfig())
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.run(10 * time.Second) // path failed, now in backoff
	before := s.sent[d]
	s.m.Restart(s.now)
	s.run(time.Second)
	if s.sent[d]-before < 3 {
		t.Fatal("restart did not trigger a new probe burst")
	}
}

func TestRelayOnlyNeverSelectsDirect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RelayOnly = true
	s := newSim(t, cfg)
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 80*time.Millisecond, 5*time.Millisecond
	s.run(5 * time.Second)
	if s.active().Kind != SharedRelay {
		t.Fatal("relay-only selected a direct path")
	}
	if s.sent[d] != 0 {
		t.Fatal("relay-only probed a direct path")
	}
}

func directPaths(m *Manager) int {
	n := 0
	for _, p := range m.Paths(0) {
		if p.Kind == Direct {
			n++
		}
	}
	return n
}

// A peer that announces Relay only answers no direct probes; the working
// direct path to it must go, not linger as the "last path" (privacy promise
// of the setting: members see only the relay's address).
func TestPeerRelayOnlyDropsDirectPaths(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 80*time.Millisecond, 5*time.Millisecond
	s.run(5 * time.Second)
	if s.active().Kind != Direct {
		t.Fatal("setup: direct path not selected")
	}
	s.m.SetPeerRelayOnly(true)
	sent := s.sent[d]
	s.run(5 * time.Second)
	if s.active().Kind != SharedRelay {
		t.Fatalf("active %v after the peer turned Relay only on", s.active().Kind)
	}
	if directPaths(s.m) != 0 || s.sent[d] != sent {
		t.Fatal("direct path kept or probed after the peer turned Relay only on")
	}
	if id, added := s.m.AddDirect(addrB, SourcePeerReflexive, s.now); added || id != 0 {
		t.Fatal("added a direct path to a Relay only peer")
	}
	s.m.SetPeerRelayOnly(false)
	if _, added := s.m.AddDirect(addrB, SourceSTUN, s.now); !added {
		t.Fatal("direct paths stay off after the peer turned Relay only off")
	}
}

func TestRelayOnlySettingDropsActiveDirect(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 80*time.Millisecond, 5*time.Millisecond
	s.run(5 * time.Second)
	cfg := DefaultConfig()
	cfg.RelayOnly = true
	s.m.SetConfig(cfg)
	s.run(3 * time.Second)
	if s.active().Kind != SharedRelay || directPaths(s.m) != 0 {
		t.Fatal("turning Relay only on kept the direct path")
	}
}

func TestMemberRelayVersusSharedAndTCPPenalty(t *testing.T) {
	via, _ := identity.Generate()
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelayTCP, true, s.now)
	mr := s.m.SetMemberRelay(via.Public(), addrB, 10*time.Millisecond, s.now)
	s.rtt[r], s.rtt[mr] = 30*time.Millisecond, 35*time.Millisecond
	s.run(5 * time.Second)
	a := s.active()
	if a.Kind != MemberRelay || a.Via != via.Public() {
		t.Fatalf("active %v; TCP fallback should carry a penalty", a.Kind)
	}
	s.m.RemoveMemberRelay(via.Public())
	s.run(100 * time.Millisecond)
	if s.active().Kind != SharedRelayTCP {
		t.Fatal("did not fall back after member relay removal")
	}
}

func TestNoFlappingBetweenSimilarPaths(t *testing.T) {
	s := newSim(t, DefaultConfig())
	a, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	b, _ := s.m.AddDirect(addrB, SourceLocal, s.now)
	s.rtt[a], s.rtt[b] = 10*time.Millisecond, 10*time.Millisecond+500*time.Microsecond
	s.run(4 * time.Second)
	first := s.active().ID
	switches := 0
	for i := 0; i < 100; i++ {
		s.run(100 * time.Millisecond)
		if s.active().ID != first {
			switches++
			first = s.active().ID
		}
	}
	if switches > 0 {
		t.Fatalf("flapped %d times", switches)
	}
}

func TestStatsAndQuality(t *testing.T) {
	s := newSim(t, DefaultConfig())
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[d] = 30 * time.Millisecond
	s.run(61 * time.Second)
	st := s.m.Stats(s.now)
	if st.Kind != Direct || st.Samples < 100 {
		t.Fatalf("stats %+v", st)
	}
	if st.AvgRTT != 30*time.Millisecond || st.LatestRTT != 30*time.Millisecond || st.Jitter != 0 || st.Loss != 0 {
		t.Fatalf("stats %+v", st)
	}
	if st.Quality != Good {
		t.Fatalf("quality %v", st.Quality)
	}

	// 10% loss makes the path Poor.
	s.lossMod[d] = 10
	s.run(61 * time.Second)
	st = s.m.Stats(s.now)
	if math.Abs(st.Loss-0.10) > 0.02 || st.Quality != Poor {
		t.Fatalf("loss %.3f quality %v", st.Loss, st.Quality)
	}
}

func TestJitterAndFairQuality(t *testing.T) {
	s := newSim(t, DefaultConfig())
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	// Alternate 70 ms and 90 ms: average 80 ms, jitter 20 ms -> Fair.
	flip := false
	for i := 0; i < 130; i++ {
		if flip {
			s.rtt[d] = 90 * time.Millisecond
		} else {
			s.rtt[d] = 70 * time.Millisecond
		}
		flip = !flip
		s.run(500 * time.Millisecond)
	}
	st := s.m.Stats(s.now)
	if st.AvgRTT < 75*time.Millisecond || st.AvgRTT > 85*time.Millisecond {
		t.Fatalf("avg %v", st.AvgRTT)
	}
	if st.Jitter < 15*time.Millisecond || st.Jitter > 21*time.Millisecond {
		t.Fatalf("jitter %v", st.Jitter)
	}
	if st.Quality != Fair {
		t.Fatalf("quality %v", st.Quality)
	}
}

func TestInboundKeepsActivePathAlive(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 60*time.Millisecond, 20*time.Millisecond
	s.run(4 * time.Second)
	// Probes are lost but WireGuard data still arrives on the path.
	s.down[d] = true
	for i := 0; i < 50; i++ {
		s.m.OnInbound(d, s.now)
		s.run(100 * time.Millisecond)
	}
	if s.active().Kind != Direct {
		t.Fatal("path with live inbound traffic was abandoned")
	}
}

// switchesDuring counts active path changes over d, sampled every 10 ms,
// with an optional hook run before each step.
func (s *sim) switchesDuring(d time.Duration, before func()) int {
	n := 0
	last := s.m.ActiveVersion()
	for end := s.now + int64(d); s.now < end; {
		if before != nil {
			before()
		}
		s.run(10 * time.Millisecond)
		if v := s.m.ActiveVersion(); v != last {
			n += int(v - last)
			last = v
		}
	}
	return n
}

// A direct path that stops answering must fail over once, not bounce
// between "alive as standby" (16 s) and "dead as active" (2 s) on every tick.
func TestDeadDirectFailsOverOnce(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 60*time.Millisecond, 20*time.Millisecond
	s.run(5 * time.Second)
	if s.active().Kind != Direct {
		t.Fatal("expected direct")
	}
	s.down[d] = true
	if n := s.switchesDuring(20*time.Second, nil); n != 1 {
		t.Fatalf("%d path changes after the direct path died, want 1", n)
	}
	if s.active().Kind != SharedRelay {
		t.Fatalf("active %v", s.active().Kind)
	}
}

// The engine reports the time of the last WireGuard packet on every tick,
// even when it is old. That must not erase newer probe replies (2026-11-04
// field test: ~18,000 direct/relay switches in 30 minutes).
func TestStaleInboundDoesNotFlap(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.rtt[r], s.rtt[d] = 150*time.Millisecond, 30*time.Millisecond
	s.run(5 * time.Second)
	if s.active().Kind != Direct {
		t.Fatal("expected direct")
	}
	stale := s.now // last WireGuard packet on the direct path, never refreshed
	n := s.switchesDuring(30*time.Second, func() { s.m.OnInbound(d, stale) })
	if n != 0 || s.active().Kind != Direct {
		t.Fatalf("%d path changes, active %v; want a stable direct path", n, s.active().Kind)
	}
}

// A better standby path replaces a working active path only right after it
// answered, and it stays active afterwards.
func TestPromotionToDirectIsStable(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	s.rtt[r] = 150 * time.Millisecond
	s.run(5 * time.Second)
	d, _ := s.m.AddDirect(addrA, SourceSTUN, s.now)
	s.down[d] = true
	s.run(10 * time.Second) // burst fails, the path backs off
	s.down[d] = false
	s.rtt[d] = 30 * time.Millisecond
	s.m.Restart(s.now)
	if n := s.switchesDuring(30*time.Second, nil); n != 1 {
		t.Fatalf("%d path changes, want 1", n)
	}
	if s.active().Kind != Direct {
		t.Fatalf("active %v", s.active().Kind)
	}
}

func TestDuplicateCandidatesAndLimit(t *testing.T) {
	m := New(DefaultConfig())
	id1, fresh := m.AddDirect(addrA, SourceSTUN, 0)
	id2, fresh2 := m.AddDirect(addrA, SourceCache, 0)
	if !fresh || fresh2 || id1 != id2 {
		t.Fatal("duplicate candidate created a second path")
	}
	for i := 0; i < 100; i++ {
		m.AddDirect(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i / 250), byte(i)}), 1), SourcePeerReflexive, int64(i))
	}
	if n := len(m.Paths(0)); n > MaxDirectPaths+2 {
		t.Fatalf("%d paths kept", n)
	}
}

func TestMultipleSharedRelaysPickLowestRTT(t *testing.T) {
	s := newSim(t, DefaultConfig())
	r0 := s.m.SetSharedRelay(0, SharedRelay, true, s.now)
	r1 := s.m.SetSharedRelay(1, SharedRelay, true, s.now)
	s.rtt[r0], s.rtt[r1] = 150*time.Millisecond, 20*time.Millisecond
	s.run(10 * time.Millisecond)
	if a := s.active(); a.Relay != 0 {
		t.Fatalf("bootstrap should use the first relay, got %d", a.Relay)
	}
	s.run(4 * time.Second)
	if a := s.active(); a.ID != r1 {
		t.Fatalf("active relay %d, want the faster relay 1", a.Relay)
	}
	// Relay 1 becomes unreachable: traffic moves back to relay 0.
	s.m.SetSharedRelay(1, 0, false, s.now)
	s.run(100 * time.Millisecond)
	if a := s.active(); a.ID != r0 {
		t.Fatalf("did not fall back to relay 0")
	}
}

func TestFindMemberRelay(t *testing.T) {
	a, _ := identity.Generate()
	b, _ := identity.Generate()
	m := New(DefaultConfig())
	m.AddDirect(addrA, SourceSTUN, 0)
	id := m.SetMemberRelay(a.Public(), addrB, time.Millisecond, 0)
	if got, ok := m.FindMemberRelay(a.Public()); !ok || got != id {
		t.Fatalf("got %v %v, want %v", got, ok, id)
	}
	if _, ok := m.FindMemberRelay(b.Public()); ok {
		t.Fatal("found a path through an unknown member")
	}
	if n := testing.AllocsPerRun(100, func() { m.FindMemberRelay(a.Public()) }); n != 0 {
		t.Fatalf("%v allocations per lookup", n)
	}
}

// Member relay offers expire; expiring several at once must not skip any.
func TestExpiredOffersAllRemoved(t *testing.T) {
	m := New(DefaultConfig())
	var vias []identity.PublicKey
	for i := 0; i < 3; i++ {
		k, _ := identity.Generate()
		vias = append(vias, k.Public())
		m.SetMemberRelay(k.Public(), addrB, time.Millisecond, 0)
	}
	m.AddDirect(addrA, SourceSTUN, 0)
	m.Tick(int64(DefaultConfig().OfferTTL) + 1)
	for _, v := range vias {
		if _, ok := m.FindMemberRelay(v); ok {
			t.Fatal("expired offer kept")
		}
	}
	if _, ok := m.FindDirect(addrA); !ok {
		t.Fatal("direct path removed")
	}
}
