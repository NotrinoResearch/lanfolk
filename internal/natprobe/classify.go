// Package natprobe measures the NAT behavior of an internet line: UDP mapping
// behavior, mapping timeout, relay reachability, IPv6 and DHT access. The
// pure classification logic lives in this file so it can be tested without a
// network.
package natprobe

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Mapping behaviors.
const (
	MappingEasy      = "easy"      // endpoint-independent mapping: same IP:port for every destination
	MappingHard      = "hard"      // endpoint-dependent mapping (symmetric): the port depends on the destination
	MappingIPVaries  = "ip-varies" // same port but the external IP differs per destination (NAT pool)
	MappingUnknown   = "unknown"   // fewer than two answers
	MaxKeepaliveSecs = 15          // keepalive cap from the implementation plan
	nonMonotonicNote = "non-monotonic result (packet loss?); only the contiguous survivors were used"
)

// Obs is one STUN answer from one destination, seen from one local socket.
type Obs struct {
	Label  string         `json:"server"` // "relay" or the configured STUN server name
	Dst    netip.AddrPort `json:"-"`      // destination as probed (never printed for the relay)
	Mapped netip.AddrPort `json:"-"`
	// MappedStr is Mapped formatted for JSON; empty when there was no answer.
	MappedStr string  `json:"mapped,omitempty"`
	RTTMs     float64 `json:"rtt_ms,omitempty"`
	Err       string  `json:"error,omitempty"`
}

// MappingClass is the result of ClassifyMapping.
type MappingClass struct {
	Behavior      string `json:"behavior"`
	PortPreserved string `json:"port_preserved"` // yes, no, partial or unknown
	IPDiffers     bool   `json:"ip_differs"`     // mapped IP differs between destinations
	Answers       int    `json:"answers"`
	DistinctDst   int    `json:"distinct_destination_ips"`
	Note          string `json:"note,omitempty"`
}

// ClassifyMapping classifies the NAT mapping from the answers that one local
// socket got from several destinations.
func ClassifyMapping(localPort uint16, obs []Obs) MappingClass {
	var c MappingClass
	ports := map[uint16]bool{}
	ips := map[netip.Addr]bool{}
	dsts := map[netip.Addr]bool{}
	preserved := 0
	for _, o := range obs {
		if !o.Mapped.IsValid() {
			continue
		}
		c.Answers++
		ports[o.Mapped.Port()] = true
		ips[o.Mapped.Addr().Unmap()] = true
		dsts[o.Dst.Addr().Unmap()] = true
		if o.Mapped.Port() == localPort {
			preserved++
		}
	}
	c.DistinctDst = len(dsts)
	c.IPDiffers = len(ips) > 1
	switch {
	case c.Answers == 0:
		c.Behavior, c.PortPreserved = MappingUnknown, "unknown"
		c.Note = "no STUN answer"
		return c
	case preserved == c.Answers:
		c.PortPreserved = "yes"
	case preserved == 0:
		c.PortPreserved = "no"
	default:
		c.PortPreserved = "partial"
	}
	switch {
	case c.Answers < 2:
		c.Behavior = MappingUnknown
		c.Note = "only one destination answered; cannot compare"
	case len(ports) > 1:
		c.Behavior = MappingHard
	case c.IPDiffers:
		c.Behavior = MappingIPVaries
		c.Note = "external IP changes with the destination; hole punching is unreliable"
	default:
		c.Behavior = MappingEasy
	}
	if c.Behavior != MappingUnknown && c.DistinctDst < 2 {
		c.Note = "all answers came from one destination IP, so address-dependent mapping cannot be ruled out"
	}
	return c
}

// AllocClass describes how the NAT picks external ports.
type AllocClass struct {
	Pattern string `json:"pattern"` // preserved, sequential, random or unknown
	Deltas  []int  `json:"deltas,omitempty"`
}

func (a AllocClass) String() string {
	if len(a.Deltas) == 0 || a.Pattern != "sequential" {
		return a.Pattern
	}
	s := make([]string, len(a.Deltas))
	for i, d := range a.Deltas {
		s[i] = fmt.Sprintf("%+d", d)
	}
	return a.Pattern + " (" + strings.Join(s, ",") + ")"
}

// ClassifyAllocation looks at the external ports of two sockets, in the order
// the NAT allocated them. m1 and m2 are the mapped ports of the first and
// second socket (one per destination, in query order; repeated ports within a
// socket are one allocation).
func ClassifyAllocation(local1 uint16, m1 []uint16, local2 uint16, m2 []uint16) AllocClass {
	seq := append(distinctRuns(m1), distinctRuns(m2)...)
	if len(seq) == 0 {
		return AllocClass{Pattern: "unknown"}
	}
	if allEqual(m1, local1) && allEqual(m2, local2) && len(m2) > 0 {
		return AllocClass{Pattern: "preserved"}
	}
	if len(seq) < 2 {
		return AllocClass{Pattern: "unknown"}
	}
	var deltas []int
	sequential := true
	for i := 1; i < len(seq); i++ {
		d := int(seq[i]) - int(seq[i-1])
		deltas = append(deltas, d)
		if d < 1 || d > 32 {
			sequential = false
		}
	}
	if sequential {
		return AllocClass{Pattern: "sequential", Deltas: deltas}
	}
	return AllocClass{Pattern: "random", Deltas: deltas}
}

func allEqual(ports []uint16, want uint16) bool {
	if len(ports) == 0 {
		return false
	}
	for _, p := range ports {
		if p != want {
			return false
		}
	}
	return true
}

// distinctRuns removes consecutive duplicates.
func distinctRuns(p []uint16) []uint16 {
	var out []uint16
	for i, v := range p {
		if i == 0 || v != p[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// TimeoutResult is the outcome for one idle time.
type TimeoutResult struct {
	Seconds   int     `json:"seconds"`
	Survived  bool    `json:"survived"`
	ArrivedMs float64 `json:"arrived_after_ms,omitempty"`
	Err       string  `json:"error,omitempty"`
}

// KeepaliveRec is the keepalive recommendation derived from the timeouts.
type KeepaliveRec struct {
	// LargestSurvived is the largest idle time such that it and every
	// smaller tested idle time survived. Zero if even the smallest failed.
	LargestSurvived int `json:"largest_survived_s"`
	// FirstFailed is the smallest idle time that did not survive, 0 if none.
	FirstFailed int    `json:"first_failed_s"`
	IntervalSec int    `json:"keepalive_s"`
	Note        string `json:"note,omitempty"`
}

// RecommendKeepalive picks a keepalive interval: half of the largest
// surviving idle time, capped at MaxKeepaliveSecs. Results with an error are
// ignored.
func RecommendKeepalive(results []TimeoutResult) KeepaliveRec {
	var rs []TimeoutResult
	for _, r := range results {
		if r.Err == "" {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Seconds < rs[j].Seconds })
	rec := KeepaliveRec{IntervalSec: MaxKeepaliveSecs}
	if len(rs) == 0 {
		rec.Note = "no timeout data; keep the default interval"
		return rec
	}
	broke := false
	for _, r := range rs {
		switch {
		case !broke && r.Survived:
			rec.LargestSurvived = r.Seconds
		case !r.Survived:
			if !broke {
				rec.FirstFailed = r.Seconds
			}
			broke = true
		default: // survived after an earlier failure
			rec.Note = nonMonotonicNote
		}
	}
	if rec.LargestSurvived == 0 {
		rec.IntervalSec = rs[0].Seconds / 2
		if rec.IntervalSec < 1 {
			rec.IntervalSec = 1
		}
		rec.Note = fmt.Sprintf("the mapping expired within %d s; the real timeout is shorter, retest with smaller values", rs[0].Seconds)
		return rec
	}
	rec.IntervalSec = rec.LargestSurvived / 2
	if rec.IntervalSec > MaxKeepaliveSecs {
		rec.IntervalSec = MaxKeepaliveSecs
	}
	if rec.IntervalSec < 1 {
		rec.IntervalSec = 1
	}
	return rec
}

// TimeoutSummary renders the measured mapping timeout, for example ">=120 s",
// "<15 s" or "60-90 s".
func (k KeepaliveRec) TimeoutSummary() string {
	switch {
	case k.LargestSurvived == 0 && k.FirstFailed == 0:
		return "n/a"
	case k.LargestSurvived == 0:
		return fmt.Sprintf("<%d s", k.FirstFailed)
	case k.FirstFailed == 0:
		return fmt.Sprintf(">=%d s", k.LargestSurvived)
	}
	return fmt.Sprintf("%d-%d s", k.LargestSurvived, k.FirstFailed)
}

// CGNATReport is the NAT and CGNAT hint.
type CGNATReport struct {
	BehindNAT string `json:"behind_nat"` // yes, no or unknown
	CGNAT     string `json:"cgnat"`      // yes, likely, no or unknown
	Detail    string `json:"detail"`
}

var cgnatRange = netip.MustParsePrefix("100.64.0.0/10")

// CGNATHint compares the addresses of the local interfaces with the public
// IP that STUN saw. wan is the optional router WAN address; without it the
// answer for CGNAT is only "yes" or "likely" when there is clear evidence.
func CGNATHint(local []netip.Addr, mapped, wan netip.Addr) CGNATReport {
	r := CGNATReport{BehindNAT: "unknown", CGNAT: "unknown"}
	if !mapped.IsValid() {
		r.Detail = "no STUN answer, so the public IP is unknown"
		return r
	}
	mapped = mapped.Unmap()
	onIface := false
	carrier := false
	for _, a := range local {
		a = a.Unmap()
		if a == mapped {
			onIface = true
		}
		if cgnatRange.Contains(a) {
			carrier = true
		}
	}
	if onIface {
		r.BehindNAT, r.CGNAT = "no", "no"
		r.Detail = "the public IP is configured on a local interface"
		return r
	}
	r.BehindNAT = "yes"
	switch {
	case wan.IsValid():
		wan = wan.Unmap()
		if wan != mapped || wan.IsPrivate() || cgnatRange.Contains(wan) {
			r.CGNAT = "yes"
			r.Detail = "the router WAN address differs from the public IP seen by STUN (or is private / 100.64.0.0/10)"
		} else {
			r.CGNAT = "no"
			r.Detail = "the router WAN address equals the public IP seen by STUN (single NAT)"
		}
	case carrier:
		r.CGNAT = "likely"
		r.Detail = "a local interface has an address in 100.64.0.0/10 (carrier-grade NAT space)"
	default:
		r.Detail = "the public IP is not on any interface, so a NAT exists; CGNAT cannot be proven without the router WAN IP (pass -wan-ip, or compare it with the public IP in your router page)"
	}
	return r
}
