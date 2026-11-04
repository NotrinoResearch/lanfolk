package discovery

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/anacrolix/torrent/bencode"
)

// newPairs returns the two views (a's and b's) of the same pair.
func newPairs(t testing.TB) (a, b *identity.Pair) {
	t.Helper()
	ka, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	a, err = identity.DerivePair(ka, kb.Public())
	if err != nil {
		t.Fatal(err)
	}
	b, err = identity.DerivePair(kb, ka.Public())
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func v4(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestRecordRoundTrip(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	rec := Record{
		Published: now,
		Candidates: []netip.AddrPort{
			v4("203.0.113.7:51820"), v4("192.168.1.20:51820"), v4("[2001:db8::1]:51820"),
		},
	}
	val, err := SealRecord(a, rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRecord(b, val, time.Hour, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Published.Equal(rec.Published) || len(got.Candidates) != len(rec.Candidates) {
		t.Fatalf("got %+v want %+v", got, rec)
	}
	for i := range got.Candidates {
		if got.Candidates[i] != rec.Candidates[i] {
			t.Fatalf("candidate %d: %v != %v", i, got.Candidates[i], rec.Candidates[i])
		}
	}
	// Empty candidate lists are valid.
	val, err = SealRecord(a, Record{Published: now})
	if err != nil {
		t.Fatal(err)
	}
	if got, err = OpenRecord(b, val, time.Hour, now); err != nil || len(got.Candidates) != 0 {
		t.Fatalf("empty record: %+v %v", got, err)
	}
}

func TestRecordNotReadableByPublisherSide(t *testing.T) {
	a, _ := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	val, _ := SealRecord(a, Record{Published: now, Candidates: []netip.AddrPort{v4("1.2.3.4:5")}})
	// The publisher's own view opens records of the remote, not its own.
	if _, err := OpenRecord(a, val, time.Hour, now); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("expected ErrRecordInvalid, got %v", err)
	}
}

func TestRecordWrongPair(t *testing.T) {
	a, _ := newPairs(t)
	_, other := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	val, _ := SealRecord(a, Record{Published: now, Candidates: []netip.AddrPort{v4("1.2.3.4:5")}})
	if _, err := OpenRecord(other, val, time.Hour, now); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("expected ErrRecordInvalid, got %v", err)
	}
}

func TestRecordTamper(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	val, _ := SealRecord(a, Record{Published: now, Candidates: []netip.AddrPort{v4("1.2.3.4:5")}})
	for i := range val {
		bad := bytes.Clone(val)
		bad[i] ^= 0x01
		if _, err := OpenRecord(b, bad, time.Hour, now); err == nil {
			t.Fatalf("tampering with byte %d not detected", i)
		}
	}
	for _, n := range []int{0, 1, 24, 40, len(val) - 1} {
		if _, err := OpenRecord(b, val[:n], time.Hour, now); err == nil {
			t.Fatalf("truncated record (%d bytes) accepted", n)
		}
	}
}

func TestRecordStale(t *testing.T) {
	a, b := newPairs(t)
	pub := time.Unix(1_800_000_000, 0)
	val, _ := SealRecord(a, Record{Published: pub, Candidates: []netip.AddrPort{v4("1.2.3.4:5")}})
	if _, err := OpenRecord(b, val, time.Hour, pub.Add(2*time.Hour)); !errors.Is(err, ErrRecordStale) {
		t.Fatalf("expected ErrRecordStale, got %v", err)
	}
	if _, err := OpenRecord(b, val, time.Hour, pub.Add(59*time.Minute)); err != nil {
		t.Fatalf("fresh record rejected: %v", err)
	}
	if _, err := OpenRecord(b, val, 0, pub.Add(1000*time.Hour)); err != nil {
		t.Fatalf("maxAge 0 must disable the check: %v", err)
	}
}

func TestRecordRejectsInvalid(t *testing.T) {
	a, _ := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	if _, err := SealRecord(a, Record{}); err == nil {
		t.Fatal("zero publication time accepted")
	}
	if _, err := SealRecord(a, Record{Published: now, Candidates: []netip.AddrPort{{}}}); err == nil {
		t.Fatal("invalid candidate accepted")
	}
}

func TestRecordCandidateCap(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	var cands []netip.AddrPort
	for i := 0; i < 200; i++ {
		var ip [16]byte
		ip[0], ip[1], ip[15] = 0x20, 0x01, byte(i)
		cands = append(cands, netip.AddrPortFrom(netip.AddrFrom16(ip), uint16(40000+i)))
	}
	val, err := SealRecord(a, Record{Published: now, Candidates: cands})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := bencode.Marshal(val)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > MaxValueSize {
		t.Fatalf("bencoded value is %d bytes, limit %d", len(enc), MaxValueSize)
	}
	got, err := OpenRecord(b, val, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != MaxRecordCandidates {
		t.Fatalf("got %d candidates, want %d", len(got.Candidates), MaxRecordCandidates)
	}
	for i, c := range got.Candidates {
		if c != cands[i] {
			t.Fatalf("candidate %d reordered: %v != %v", i, c, cands[i])
		}
	}
	if bencodedLen(len(val)) != len(enc) {
		t.Fatalf("bencodedLen %d != %d", bencodedLen(len(val)), len(enc))
	}
}
