package discovery

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

func lookupFor(p *identity.Pair) func(identity.KeyID) *identity.Pair {
	return func(id identity.KeyID) *identity.Pair {
		if p.Remote.KeyID() == id {
			return p
		}
		return nil
	}
}

func TestConnCodeRoundTrip(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	cands := []netip.AddrPort{v4("203.0.113.7:51820"), v4("[2001:db8::1]:51820"), v4("192.168.1.2:1")}
	code, err := CreateConnCode(a, cands, 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, ConnCodePrefix) {
		t.Fatalf("code %q lacks prefix", code)
	}
	sender, got, exp, err := ParseConnCode(code, lookupFor(b), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sender != a.Local {
		t.Fatal("wrong sender")
	}
	if !exp.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("expiry %v", exp)
	}
	if len(got) != len(cands) {
		t.Fatalf("got %v", got)
	}
	for i := range got {
		if got[i] != cands[i] {
			t.Fatalf("candidate %d: %v != %v", i, got[i], cands[i])
		}
	}
}

func TestConnCodeExpiry(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("1.2.3.4:5")}, time.Minute, now)
	if _, _, _, err := ParseConnCode(code, lookupFor(b), now.Add(59*time.Second)); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if _, _, _, err := ParseConnCode(code, lookupFor(b), now.Add(time.Minute)); !errors.Is(err, ErrConnCodeExpired) {
		t.Fatalf("expected ErrConnCodeExpired, got %v", err)
	}
	if _, err := CreateConnCode(a, []netip.AddrPort{v4("1.2.3.4:5")}, 0, now); err == nil {
		t.Fatal("zero ttl accepted")
	}
}

func TestConnCodeExpiryAuthenticated(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("1.2.3.4:5")}, time.Minute, now)
	raw, err := zbase32.DecodeString(strings.TrimPrefix(code, ConnCodePrefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[11] += 8 // extend the expiry by a few seconds
	forged := ConnCodePrefix + zbase32.EncodeToString(raw)
	if _, _, _, err := ParseConnCode(forged, lookupFor(b), now); !errors.Is(err, ErrConnCodeInvalid) {
		t.Fatalf("expiry tampering not detected: %v", err)
	}
}

func TestConnCodeWrongRecipient(t *testing.T) {
	a, _ := newPairs(t)
	_, c := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("1.2.3.4:5")}, time.Hour, now)

	// Lookup claims a pair that is not the sender's.
	wrong := func(identity.KeyID) *identity.Pair { return c }
	if _, _, _, err := ParseConnCode(code, wrong, now); !errors.Is(err, ErrConnCodeInvalid) {
		t.Fatalf("expected ErrConnCodeInvalid, got %v", err)
	}

	// A lookup that matches the key ID but holds different pair secrets.
	_, other := newPairs(t)
	spoof := func(identity.KeyID) *identity.Pair { return &identity.Pair{Local: other.Local, Remote: a.Local} }
	if _, _, _, err := ParseConnCode(code, spoof, now); err == nil {
		t.Fatal("code opened with another pair's secret")
	}
}

func TestConnCodeUnknownSender(t *testing.T) {
	a, _ := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("1.2.3.4:5")}, time.Hour, now)
	none := func(identity.KeyID) *identity.Pair { return nil }
	if _, _, _, err := ParseConnCode(code, none, now); !errors.Is(err, ErrConnCodeUnknown) {
		t.Fatalf("expected ErrConnCodeUnknown, got %v", err)
	}
}

func TestConnCodeMessyInput(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("203.0.113.7:51820")}, time.Hour, now)
	var spaced strings.Builder
	for i, r := range code {
		if i > 0 && i%5 == 0 {
			spaced.WriteString(" - ")
		}
		spaced.WriteRune(r)
	}
	for _, in := range []string{
		"  " + strings.ToUpper(code) + "\n",
		spaced.String(),
		"\t" + strings.ReplaceAll(code, "-", "") + "\r\n",
	} {
		if _, _, _, err := ParseConnCode(in, lookupFor(b), now); err != nil {
			t.Fatalf("messy input %q: %v", in, err)
		}
	}
}

func TestConnCodeGarbage(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	code, _ := CreateConnCode(a, []netip.AddrPort{v4("203.0.113.7:51820")}, time.Hour, now)
	for _, in := range []string{
		"", "lanfolk-c1", "lanfolk-c1!!!!", "hello", a.Remote.FriendCode(),
		code[:len(code)-3], code + "yy",
	} {
		if _, _, _, err := ParseConnCode(in, lookupFor(b), now); err == nil {
			t.Fatalf("garbage %q accepted", in)
		}
	}
	// Flipping any body character must never produce a valid code.
	body := []byte(code)
	for i := len(ConnCodePrefix); i < len(body); i++ {
		orig := body[i]
		body[i] = 'y'
		if orig == 'y' {
			body[i] = 'b'
		}
		if _, _, _, err := ParseConnCode(string(body), lookupFor(b), now); err == nil {
			t.Fatalf("flipped character %d accepted", i)
		}
		body[i] = orig
	}
}

func TestConnCodeCandidateCapAndSize(t *testing.T) {
	a, b := newPairs(t)
	now := time.Unix(1_800_000_000, 0)
	var cands []netip.AddrPort
	for i := 0; i < 20; i++ {
		var ip [16]byte
		ip[0], ip[1], ip[15] = 0x20, 0x01, byte(i)
		cands = append(cands, netip.AddrPortFrom(netip.AddrFrom16(ip), uint16(40000+i)))
	}
	code, err := CreateConnCode(a, cands, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	_, got, _, err := ParseConnCode(code, lookupFor(b), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxConnCodeCandidates {
		t.Fatalf("got %d candidates, want %d", len(got), MaxConnCodeCandidates)
	}
	if len(code) > 300 {
		t.Fatalf("worst-case code is %d characters", len(code))
	}
	if _, err := CreateConnCode(a, nil, time.Hour, now); err == nil {
		t.Fatal("empty candidate list accepted")
	}
}
