package natprobe

import (
	"net/netip"
	"testing"
)

func obs(dst, mapped string) Obs {
	o := Obs{Label: dst, Dst: netip.MustParseAddrPort(dst)}
	if mapped != "" {
		o.Mapped = netip.MustParseAddrPort(mapped)
	}
	return o
}

func TestClassifyMapping(t *testing.T) {
	cases := []struct {
		name      string
		local     uint16
		obs       []Obs
		behavior  string
		preserved string
		ipDiffers bool
		distinct  int
	}{
		{"easy, port preserved", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:40000"), obs("2.2.2.2:19302", "9.9.9.9:40000"), obs("3.3.3.3:3478", "9.9.9.9:40000")},
			MappingEasy, "yes", false, 3},
		{"easy, port changed once", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:51234"), obs("2.2.2.2:19302", "9.9.9.9:51234")},
			MappingEasy, "no", false, 2},
		{"hard, symmetric", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:51234"), obs("2.2.2.2:19302", "9.9.9.9:51240"), obs("3.3.3.3:3478", "9.9.9.9:51241")},
			MappingHard, "no", false, 3},
		{"hard, preserved for one destination only", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:40000"), obs("2.2.2.2:19302", "9.9.9.9:40001")},
			MappingHard, "partial", false, 2},
		{"same port, IP differs", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:40000"), obs("2.2.2.2:19302", "9.9.9.10:40000")},
			MappingIPVaries, "yes", true, 2},
		{"one answer only", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:40000"), obs("2.2.2.2:19302", "")},
			MappingUnknown, "yes", false, 1},
		{"no answer", 40000, []Obs{obs("1.1.1.1:3478", "")}, MappingUnknown, "unknown", false, 0},
		{"nothing", 40000, nil, MappingUnknown, "unknown", false, 0},
		{"unanswered servers are ignored", 40000,
			[]Obs{obs("1.1.1.1:3478", "9.9.9.9:51234"), obs("2.2.2.2:19302", ""), obs("3.3.3.3:3478", "9.9.9.9:51234")},
			MappingEasy, "no", false, 2},
	}
	for _, c := range cases {
		got := ClassifyMapping(c.local, c.obs)
		if got.Behavior != c.behavior || got.PortPreserved != c.preserved || got.IPDiffers != c.ipDiffers || got.DistinctDst != c.distinct {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestClassifyMappingSingleDestinationIP(t *testing.T) {
	// Two ports on the same IP are not enough to rule out address-dependent
	// mapping; the result carries a note.
	got := ClassifyMapping(40000, []Obs{obs("1.1.1.1:3478", "9.9.9.9:40000"), obs("1.1.1.1:19302", "9.9.9.9:40000")})
	if got.Behavior != MappingEasy || got.DistinctDst != 1 || got.Note == "" {
		t.Errorf("got %+v", got)
	}
}

func TestClassifyAllocation(t *testing.T) {
	cases := []struct {
		name    string
		l1      uint16
		m1      []uint16
		l2      uint16
		m2      []uint16
		pattern string
		str     string
	}{
		{"preserved", 40000, []uint16{40000, 40000}, 40001, []uint16{40001, 40001}, "preserved", "preserved"},
		{"sequential easy NAT", 40000, []uint16{50000, 50000}, 40001, []uint16{50001, 50001}, "sequential", "sequential (+1)"},
		{"sequential hard NAT", 40000, []uint16{50000, 50001, 50002}, 40001, []uint16{50003, 50004, 50005}, "sequential", "sequential (+1,+1,+1,+1,+1)"},
		{"stride 2", 40000, []uint16{50000}, 40001, []uint16{50002}, "sequential", "sequential (+2)"},
		{"random", 40000, []uint16{50000, 61200, 33333}, 40001, []uint16{12345, 60000, 41000}, "random", "random"},
		{"decreasing is not sequential", 40000, []uint16{50010}, 40001, []uint16{50000}, "random", "random"},
		{"one mapping", 40000, []uint16{50000}, 40001, nil, "unknown", "unknown"},
		{"nothing", 40000, nil, 40001, nil, "unknown", "unknown"},
	}
	for _, c := range cases {
		got := ClassifyAllocation(c.l1, c.m1, c.l2, c.m2)
		if got.Pattern != c.pattern || got.String() != c.str {
			t.Errorf("%s: got %q (%s), want %q (%s)", c.name, got.Pattern, got.String(), c.pattern, c.str)
		}
	}
}

func tr(sec int, ok bool) TimeoutResult { return TimeoutResult{Seconds: sec, Survived: ok} }

func TestRecommendKeepalive(t *testing.T) {
	cases := []struct {
		name     string
		in       []TimeoutResult
		largest  int
		failed   int
		interval int
		summary  string
	}{
		{"all survive", []TimeoutResult{tr(15, true), tr(30, true), tr(60, true), tr(90, true), tr(120, true)}, 120, 0, 15, ">=120 s"},
		{"fails after 60", []TimeoutResult{tr(15, true), tr(30, true), tr(60, true), tr(90, false), tr(120, false)}, 60, 90, 15, "60-90 s"},
		{"fails after 30", []TimeoutResult{tr(15, true), tr(30, true), tr(60, false)}, 30, 60, 15, "30-60 s"},
		{"fails after 15", []TimeoutResult{tr(15, true), tr(30, false), tr(60, false)}, 15, 30, 7, "15-30 s"},
		{"all fail", []TimeoutResult{tr(15, false), tr(30, false)}, 0, 15, 7, "<15 s"},
		{"unsorted input", []TimeoutResult{tr(90, false), tr(15, true), tr(60, true), tr(30, true)}, 60, 90, 15, "60-90 s"},
		{"cap at 15", []TimeoutResult{tr(15, true), tr(30, true), tr(60, true), tr(90, true), tr(120, true), tr(150, true)}, 150, 0, 15, ">=150 s"},
		{"tiny timeout", []TimeoutResult{tr(2, true), tr(4, false)}, 2, 4, 1, "2-4 s"},
		{"empty", nil, 0, 0, 15, "n/a"},
	}
	for _, c := range cases {
		got := RecommendKeepalive(c.in)
		if got.LargestSurvived != c.largest || got.FirstFailed != c.failed || got.IntervalSec != c.interval || got.TimeoutSummary() != c.summary {
			t.Errorf("%s: got %+v (%s)", c.name, got, got.TimeoutSummary())
		}
	}
	// A survivor after a failure (packet loss) is not trusted.
	got := RecommendKeepalive([]TimeoutResult{tr(15, true), tr(30, false), tr(60, true)})
	if got.LargestSurvived != 15 || got.Note == "" {
		t.Errorf("non-monotonic: %+v", got)
	}
	// Errors are ignored, not treated as expiry.
	got = RecommendKeepalive([]TimeoutResult{tr(15, true), {Seconds: 30, Err: "boom"}, tr(60, true)})
	if got.LargestSurvived != 60 || got.FirstFailed != 0 {
		t.Errorf("errors: %+v", got)
	}
}

func TestCGNATHint(t *testing.T) {
	a := netip.MustParseAddr
	cases := []struct {
		name     string
		local    []netip.Addr
		mapped   netip.Addr
		wan      netip.Addr
		nat, cgn string
	}{
		{"public IP on interface", []netip.Addr{a("203.0.113.5")}, a("203.0.113.5"), netip.Addr{}, "no", "no"},
		{"home NAT, WAN unknown", []netip.Addr{a("192.168.1.10")}, a("203.0.113.5"), netip.Addr{}, "yes", "unknown"},
		{"carrier space on interface", []netip.Addr{a("100.72.1.9")}, a("203.0.113.5"), netip.Addr{}, "yes", "likely"},
		{"WAN equals mapped", []netip.Addr{a("192.168.1.10")}, a("203.0.113.5"), a("203.0.113.5"), "yes", "no"},
		{"WAN differs", []netip.Addr{a("192.168.1.10")}, a("203.0.113.5"), a("198.51.100.7"), "yes", "yes"},
		{"WAN private", []netip.Addr{a("192.168.1.10")}, a("203.0.113.5"), a("10.20.30.40"), "yes", "yes"},
		{"WAN in 100.64/10", []netip.Addr{a("192.168.1.10")}, a("203.0.113.5"), a("100.100.1.1"), "yes", "yes"},
		{"no answer", []netip.Addr{a("192.168.1.10")}, netip.Addr{}, netip.Addr{}, "unknown", "unknown"},
		{"v4-mapped mapped address", []netip.Addr{a("203.0.113.5")}, a("::ffff:203.0.113.5"), netip.Addr{}, "no", "no"},
	}
	for _, c := range cases {
		got := CGNATHint(c.local, c.mapped, c.wan)
		if got.BehindNAT != c.nat || got.CGNAT != c.cgn {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestGlobalIPv6(t *testing.T) {
	a := netip.MustParseAddr
	got := GlobalIPv6([]netip.Addr{a("fe80::1"), a("fd00::5"), a("2a02:1234::7"), a("::1"), a("192.168.0.2"), a("2001:db8::1")})
	if len(got) != 2 {
		t.Errorf("want 2 global addresses, got %v", got)
	}
}
