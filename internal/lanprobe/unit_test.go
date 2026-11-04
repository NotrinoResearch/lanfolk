package lanprobe

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 100; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	cases := []struct {
		p    float64
		want time.Duration
	}{{0, 1 * time.Millisecond}, {1, 1 * time.Millisecond}, {50, 50 * time.Millisecond}, {95, 95 * time.Millisecond},
		{99, 99 * time.Millisecond}, {99.5, 100 * time.Millisecond}, {100, 100 * time.Millisecond}}
	for _, c := range cases {
		if got := Percentile(s, c.p); got != c.want {
			t.Errorf("p%v = %v, want %v", c.p, got, c.want)
		}
	}
	if Percentile(nil, 50) != 0 {
		t.Error("empty slice must give 0")
	}
	// Odd and tiny sets: nearest rank.
	three := []time.Duration{1, 2, 3}
	if Percentile(three, 50) != 2 || Percentile(three, 99) != 3 || Percentile(three, 1) != 1 {
		t.Errorf("small set percentiles wrong")
	}
}

func TestSummarize(t *testing.T) {
	rtts := []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond}
	orig := append([]time.Duration(nil), rtts...)
	s := Summarize(rtts, 5)
	for i := range rtts {
		if rtts[i] != orig[i] {
			t.Fatal("Summarize modified its input")
		}
	}
	if s.Min != time.Millisecond || s.Max != 5*time.Millisecond || s.P50 != 2*time.Millisecond {
		t.Errorf("summary %+v", s)
	}
	if s.Received != 4 || s.Sent != 5 || s.LossPct != 20 {
		t.Errorf("loss accounting %+v", s)
	}
	if s.Mean != 2750*time.Microsecond {
		t.Errorf("mean %v", s.Mean)
	}
	if z := Summarize(nil, 10); z.LossPct != 100 || z.Received != 0 {
		t.Errorf("all lost: %+v", z)
	}
}

func TestComputeOverhead(t *testing.T) {
	raw := Summary{P50: 20 * time.Millisecond, P99: 25 * time.Millisecond}
	ok := ComputeOverhead(raw, Summary{P50: 20800 * time.Microsecond, P99: 27 * time.Millisecond})
	if !ok.OKP50 || !ok.OKP99 {
		t.Errorf("0.8/2 ms must pass: %+v", ok)
	}
	bad := ComputeOverhead(raw, Summary{P50: 22 * time.Millisecond, P99: 30 * time.Millisecond})
	if bad.OKP50 || bad.OKP99 {
		t.Errorf("2/5 ms must fail: %+v", bad)
	}
	edge := ComputeOverhead(raw, Summary{P50: 21 * time.Millisecond, P99: 28 * time.Millisecond})
	if !edge.OKP50 || !edge.OKP99 {
		t.Errorf("exactly 1/3 ms must pass: %+v", edge)
	}
}

func TestChecksumKnownVector(t *testing.T) {
	// The IPv4 header from the Wikipedia article "IPv4 header checksum";
	// the checksum field (bytes 10-11) is 0xb861.
	hdr, _ := hex.DecodeString("450000730000400040110000c0a80001c0a800c7")
	if got := Checksum(hdr); got != 0xb861 {
		t.Fatalf("checksum %#04x, want 0xb861", got)
	}
	binary.BigEndian.PutUint16(hdr[10:], 0xb861)
	if got := Checksum(hdr); got != 0 {
		t.Fatalf("header with checksum must sum to zero, got %#04x", got)
	}
	// RFC 1071 section 3 example: 00 01 f2 03 f4 f5 f6 f7 sums to 0xddf2.
	ex, _ := hex.DecodeString("0001f203f4f5f6f7")
	if got := Checksum(ex); got != ^uint16(0xddf2) {
		t.Fatalf("RFC 1071 example: %#04x", got)
	}
	// Odd length buffers are padded with a zero byte.
	if Checksum([]byte{0x12, 0x34, 0x56}) != Checksum([]byte{0x12, 0x34, 0x56, 0x00}) {
		t.Fatal("odd length padding")
	}
}

func TestBuildIPv4UDPVector(t *testing.T) {
	// Expected bytes computed independently (Python, struct + a plain
	// one's complement sum) for IP ID 0x1234 and a 13-byte (odd) payload.
	want := "4500002912344000011151f50a4d00030a4d00ff9c401e700015365168656c6c6f206c616e666f6c6b"
	got := buildIPv4UDP(0x1234, netip.MustParseAddr("10.77.0.3"), netip.MustParseAddr("10.77.0.255"), 40000, 7792, []byte("hello lanfolk"))
	if hex.EncodeToString(got) != want {
		t.Fatalf("packet\n got %x\nwant %s", got, want)
	}
}

func TestParseIPv4UDPRoundTrip(t *testing.T) {
	src, dst := netip.MustParseAddr("10.77.0.3"), netip.MustParseAddr("239.255.77.77")
	for _, n := range []int{0, 1, 2, 13, 200, 1300} {
		payload := bytes.Repeat([]byte{0xA5}, n)
		pkt := BuildIPv4UDP(src, dst, 1234, 7793, payload)
		u, err := ParseIPv4UDP(pkt)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if u.Src != src || u.Dst != dst || u.SrcPort != 1234 || u.DstPort != 7793 || !bytes.Equal(u.Payload, payload) {
			t.Fatalf("n=%d: decoded %+v", n, u)
		}
		if !u.IPChecksumOK || !u.UDPChecksumOK {
			t.Fatalf("n=%d: checksums not valid: %+v", n, u)
		}
		// Flip one payload bit: the UDP checksum must now be wrong.
		if n > 0 {
			pkt[len(pkt)-1] ^= 1
			if u2, _ := ParseIPv4UDP(pkt); u2.UDPChecksumOK {
				t.Fatalf("n=%d: corrupted payload accepted", n)
			}
		}
	}
	// Corrupt the header.
	pkt := BuildIPv4UDP(src, dst, 1, 2, []byte("x"))
	pkt[8] ^= 0xff
	if u, _ := ParseIPv4UDP(pkt); u.IPChecksumOK {
		t.Fatal("corrupted IP header accepted")
	}
	if _, err := ParseIPv4UDP([]byte{0x60, 0, 0}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestMsgRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := &Msg{Type: MsgReportR, Token: "abc", Probes: []ProbeRecord{{Name: "x", Count: 3, Src: "10.77.0.2", Claimed: "10.77.0.2", Match: true}}, Data: []byte{1, 2, 3}}
	if err := WriteMsg(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.Token != "abc" || len(out.Probes) != 1 || out.Probes[0] != in.Probes[0] || !bytes.Equal(out.Data, in.Data) {
		t.Fatalf("round trip: %+v", out)
	}
	// An oversized length prefix is refused without allocating.
	bad := binary.BigEndian.AppendUint32(nil, MaxMsg+1)
	if _, err := ReadMsg(bytes.NewReader(bad)); err == nil {
		t.Fatal("oversized message accepted")
	}
	if _, err := ReadMsg(bytes.NewReader(binary.BigEndian.AppendUint32(nil, 0))); err == nil {
		t.Fatal("empty message accepted")
	}
	if _, err := ReadMsg(bytes.NewReader([]byte{0, 0, 0, 5, 'n', 'o', 't', 'j', 's'})); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestProbeEncoding(t *testing.T) {
	p := Probe{Token: "deadbeef", Name: IDSubnetBcast, Claimed: "10.77.0.2", Seq: 7}
	got, err := DecodeProbe(p.Encode())
	if err != nil || got != p {
		t.Fatalf("decode: %+v %v", got, err)
	}
	for _, stray := range []string{"", "hello", "LFPROBE1|a|b|c", "LFPROBE2|a|b|c|1", "LFPROBE1||b|c|1", "LFPROBE1|a|b|c|x", "LFPROBE1|a|b|c|1|extra"} {
		if _, err := DecodeProbe([]byte(stray)); err == nil {
			t.Errorf("stray %q accepted", stray)
		}
	}
}

func TestIPInPayloadResult(t *testing.T) {
	good := map[string]ProbeRecord{
		IDUDP:         {Name: IDUDP, Count: 3, Src: "10.77.0.2", Claimed: "10.77.0.2", Match: true},
		IDSubnetBcast: {Name: IDSubnetBcast, Count: 3, Src: "10.77.0.2", Claimed: "10.77.0.2", Match: true},
	}
	if r := ipInPayloadResult(good); r.Status != Pass {
		t.Errorf("good: %+v", r)
	}
	bad := map[string]ProbeRecord{
		IDUDP:         good[IDUDP],
		IDSubnetBcast: {Name: IDSubnetBcast, Count: 3, Src: "192.168.1.5", Claimed: "10.77.0.2", Match: false, BadSrc: "192.168.1.5"},
	}
	r := ipInPayloadResult(bad)
	if r.Status != Fail || !strings.Contains(r.Detail, "10.77.0.2") || !strings.Contains(r.Detail, "192.168.1.5") {
		t.Errorf("mismatch must fail and name both addresses: %+v", r)
	}
	// A mismatch only on a non-primary variant is a note, not a failure.
	note := map[string]ProbeRecord{
		IDUDP:       good[IDUDP],
		IDGlobalAny: {Name: IDGlobalAny, Count: 3, Src: "192.168.1.5", Claimed: "10.77.0.2", BadSrc: "192.168.1.5"},
	}
	if r := ipInPayloadResult(note); r.Status != Pass || !strings.Contains(r.Detail, "note") {
		t.Errorf("note: %+v", r)
	}
	if r := ipInPayloadResult(nil); r.Status != Fail {
		t.Errorf("nothing received must fail: %+v", r)
	}
}

func TestPrivateRangeResult(t *testing.T) {
	for ip, want := range map[string]Status{
		"10.77.0.3": Pass, "192.168.1.9": Pass, "172.16.5.5": Pass, "172.32.0.1": Fail,
		"100.64.1.1": Fail, "8.8.8.8": Fail, "127.0.0.1": Fail,
	} {
		if got := privateRangeResult(netip.MustParseAddr(ip)).Status; got != want {
			t.Errorf("%s: %s, want %s", ip, got, want)
		}
	}
}

func TestMarkdownRow(t *testing.T) {
	rs := []Result{{ID: IDTCP, Status: Pass}, {ID: IDUDP, Status: Fail}}
	h, r := MarkdownRow("a|b", rs)
	if !strings.HasPrefix(h, "| Setup | TCP | UDP |\n| --- | --- | --- |") || r != "| a/b | PASS | FAIL |" {
		t.Errorf("header %q row %q", h, r)
	}
}
