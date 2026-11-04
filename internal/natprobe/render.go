package natprobe

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func mappingText(c MappingClass) string {
	switch c.Behavior {
	case MappingEasy:
		return "easy (endpoint-independent mapping, hole punching should work)"
	case MappingHard:
		return "hard (endpoint-dependent mapping, symmetric NAT: use a relay)"
	case MappingIPVaries:
		return "hard (same port but the external IP differs per destination)"
	}
	return "unknown"
}

func rttText(s *RTTStats) string {
	if s == nil {
		return "n/a"
	}
	return fmt.Sprintf("min %.1f / median %.1f / p95 %.1f ms (%d of %d answered)", s.MinMs, s.MedianMs, s.P95Ms, s.Received, s.Sent)
}

func ipv6Text(r IPv6Report) string {
	switch {
	case r.GlobalAddrs == 0:
		return "no (no global IPv6 address)"
	case r.STUNOK:
		return fmt.Sprintf("yes (%d global address(es), STUN over IPv6 works, %.1f ms)", r.GlobalAddrs, r.RTTMs)
	}
	return fmt.Sprintf("address present (%d) but STUN over IPv6 failed: %s", r.GlobalAddrs, r.Err)
}

func dhtText(d DHTReport) string {
	if d.Skipped {
		return "skipped"
	}
	s := fmt.Sprintf("%d nodes (%d good)", d.Nodes, d.GoodNodes)
	if d.FirstNodeMs > 0 {
		s += fmt.Sprintf(", first answer after %.1f s", d.FirstNodeMs/1000)
	}
	if d.Nodes > 0 {
		s += "; BEP 44 put " + okText(d.PutOK, d.PutMs) + ", get " + okText(d.GetOK, d.GetMs)
	}
	if d.Err != "" {
		s += " [" + d.Err + "]"
	}
	return s
}

func okText(ok bool, ms float64) string {
	if ok {
		return fmt.Sprintf("ok (%.1f s)", ms/1000)
	}
	return "FAILED"
}

func timeoutText(t TimeoutReport) string {
	if t.Skipped != "" {
		return "skipped (" + t.Skipped + ")"
	}
	var parts []string
	for _, r := range t.Results {
		switch {
		case r.Err != "":
			parts = append(parts, fmt.Sprintf("%ds error", r.Seconds))
		case r.Survived:
			parts = append(parts, fmt.Sprintf("%ds alive", r.Seconds))
		default:
			parts = append(parts, fmt.Sprintf("%ds expired", r.Seconds))
		}
	}
	return t.Keepalive.TimeoutSummary() + "  [" + strings.Join(parts, ", ") + "]"
}

// WriteSummary prints a readable summary table.
func (r *Report) WriteSummary(w io.Writer) {
	title := "natprobe results"
	if r.ISP != "" {
		title += " for " + r.ISP
	}
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, strings.Repeat("-", len(title)))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, v) }

	row("Mapping behavior", mappingText(r.Mapping.Class))
	if r.Mapping.Error != "" {
		row("", "error: "+r.Mapping.Error)
	}
	row("Port preservation", r.Mapping.Class.PortPreserved)
	row("External IP differs by server", yesNo(r.Mapping.Class.IPDiffers))
	if r.Second != nil {
		a := r.Second.Allocation.String()
		if r.Second.Error != "" {
			a = "error: " + r.Second.Error
		}
		row("Port allocation (2 sockets)", a)
	}
	if r.Mapping.Class.Note != "" {
		row("Mapping note", r.Mapping.Class.Note)
	}
	if r.PublicIP != "" {
		row("Public IPv4 (do not share)", r.PublicIP)
	}
	for _, o := range r.Mapping.Observations {
		v := o.MappedStr
		if o.Err != "" {
			v = "no answer (" + o.Err + ")"
		} else {
			v = fmt.Sprintf("%s  (%.1f ms)", v, o.RTTMs)
		}
		row("  from socket 1 via "+o.Label, v)
	}
	if r.Relay.Configured {
		if r.Relay.UDPErr != "" {
			row("Relay UDP RTT", "FAILED: "+r.Relay.UDPErr)
		} else {
			row("Relay UDP RTT", rttText(r.Relay.UDP))
		}
		if r.Relay.TCPErr != "" {
			row("Relay TCP RTT", "FAILED: "+r.Relay.TCPErr)
		} else {
			row("Relay TCP RTT", fmt.Sprintf("connect %.1f ms; ping %s", r.Relay.TCPConnect, rttText(r.Relay.TCP)))
		}
	} else {
		row("Relay UDP / TCP RTT", "skipped (no relay)")
	}
	row("NAT mapping timeout", timeoutText(r.Timeout))
	if r.Timeout.Skipped == "" {
		ka := fmt.Sprintf("%d s", r.Timeout.Keepalive.IntervalSec)
		if r.Timeout.Keepalive.Note != "" {
			ka += "  (" + r.Timeout.Keepalive.Note + ")"
		}
		row("Recommended keepalive", ka)
	}
	row("IPv6", ipv6Text(r.IPv6))
	row("DHT", dhtText(r.DHT))
	row("Behind NAT", r.CGNAT.BehindNAT)
	row("CGNAT", r.CGNAT.CGNAT+"  ("+r.CGNAT.Detail+")")
	tw.Flush()
	for _, n := range r.Notes {
		fmt.Fprintln(w, "note:", n)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// MarkdownHeader is the header of the table in docs/MEASUREMENTS.md.
const MarkdownHeader = "| ISP | Mapping | Port preservation | Relay RTT ms (min/med/p95) | TCP RTT ms (min/med/p95) | NAT timeout (keepalive) | IPv6 | DHT nodes | DHT put/get |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- | --- |"

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ") }

func rttCell(s *RTTStats) string {
	if s == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f/%.1f/%.1f", s.MinMs, s.MedianMs, s.P95Ms)
}

// MarkdownRow returns one table row for docs/MEASUREMENTS.md. It contains no
// IP address and no relay host.
func (r *Report) MarkdownRow() string {
	isp := r.ISP
	if isp == "" {
		isp = "unknown"
	}
	pp := r.Mapping.Class.PortPreserved
	if r.Second != nil && r.Second.Allocation.Pattern != "" && r.Second.Allocation.Pattern != "unknown" && r.Second.Allocation.Pattern != "preserved" {
		pp += ", " + r.Second.Allocation.Pattern
	}
	mapping := r.Mapping.Class.Behavior
	if mapping == MappingIPVaries {
		mapping = "hard (ip varies)"
	}
	nt := "n/a"
	if r.Timeout.Skipped == "" {
		nt = fmt.Sprintf("%s (%d s)", r.Timeout.Keepalive.TimeoutSummary(), r.Timeout.Keepalive.IntervalSec)
	}
	relay, tcp := "n/a", "n/a"
	if r.Relay.Configured {
		relay, tcp = rttCell(r.Relay.UDP), rttCell(r.Relay.TCP)
	}
	v6 := "no"
	if r.IPv6.GlobalAddrs > 0 {
		v6 = "address only"
		if r.IPv6.STUNOK {
			v6 = "yes"
		}
	}
	nodes, pg := "skipped", "skipped"
	if !r.DHT.Skipped {
		nodes = fmt.Sprint(r.DHT.Nodes)
		switch {
		case r.DHT.Nodes == 0:
			pg = "n/a"
		case r.DHT.PutOK && r.DHT.GetOK:
			pg = fmt.Sprintf("ok/ok (%.1f s/%.1f s)", r.DHT.PutMs/1000, r.DHT.GetMs/1000)
		case r.DHT.PutOK:
			pg = "ok/FAILED"
		default:
			pg = "FAILED"
		}
	}
	cells := []string{isp, mapping, pp, relay, tcp, nt, v6, nodes, pg}
	for i := range cells {
		cells[i] = cell(cells[i])
	}
	return "| " + strings.Join(cells, " | ") + " |"
}
