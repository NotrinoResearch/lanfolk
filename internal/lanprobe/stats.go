// Package lanprobe implements the Lanfolk LAN behavior tests: a probe server
// and tester that check unicast, broadcast and multicast delivery between two
// machines, a UDP latency tool, and the Wintun self-test used in Phase 0.
// It also holds the small measurement helpers shared with natprobe.
package lanprobe

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Percentile returns the p-th percentile (0..100) of an ascending-sorted
// slice using the nearest-rank method. It returns 0 for an empty slice.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	rank := int(math.Ceil(p / 100 * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Summary describes a set of round trip times.
type Summary struct {
	Sent     int           `json:"sent"`
	Received int           `json:"received"`
	LossPct  float64       `json:"loss_pct"`
	Min      time.Duration `json:"min_ns"`
	P50      time.Duration `json:"p50_ns"`
	P95      time.Duration `json:"p95_ns"`
	P99      time.Duration `json:"p99_ns"`
	Max      time.Duration `json:"max_ns"`
	Mean     time.Duration `json:"mean_ns"`
}

// Summarize computes a Summary. sent is the number of probes sent; rtts holds
// the round trips of those that were answered (in any order). The input slice
// is not modified.
func Summarize(rtts []time.Duration, sent int) Summary {
	s := Summary{Sent: sent, Received: len(rtts)}
	if sent > 0 {
		s.LossPct = 100 * float64(sent-len(rtts)) / float64(sent)
		if s.LossPct < 0 {
			s.LossPct = 0
		}
	}
	if len(rtts) == 0 {
		return s
	}
	sorted := append([]time.Duration(nil), rtts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	s.Min = sorted[0]
	s.Max = sorted[len(sorted)-1]
	s.P50 = Percentile(sorted, 50)
	s.P95 = Percentile(sorted, 95)
	s.P99 = Percentile(sorted, 99)
	s.Mean = sum / time.Duration(len(sorted))
	return s
}

// Ms formats a duration in milliseconds with microsecond precision.
func Ms(d time.Duration) string {
	return fmt.Sprintf("%.3f ms", float64(d)/float64(time.Millisecond))
}
