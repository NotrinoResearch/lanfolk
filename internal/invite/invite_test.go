package invite

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
)

func newCode(t *testing.T, ttl time.Duration, now time.Time) (Code, *identity.PrivateKey) {
	t.Helper()
	k, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(k.Public(), network.ID{1, 2, 3}, ttl, now)
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestCodeRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, k := newCode(t, TTL, now)
	s := c.String()
	if !strings.HasPrefix(s, Prefix) {
		t.Fatalf("code %q lacks prefix", s)
	}
	got, err := Parse(s, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != c || got.Owner != k.Public() || !got.Expires.Equal(now.Add(TTL)) {
		t.Fatalf("round trip changed the code: %+v vs %+v", got, c)
	}
	// Spaces, dashes, line breaks and case survive a chat message.
	mangled := strings.ToUpper(s[:20]) + " -\n" + s[20:40] + "\t" + s[40:]
	if got2, err := Parse(mangled, now); err != nil || got2 != c {
		t.Fatalf("mangled code: %v", err)
	}
}

func TestCodeExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, _ := newCode(t, time.Minute, now)
	if _, err := Parse(c.String(), now.Add(59*time.Second)); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if _, err := Parse(c.String(), now.Add(time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired code: %v", err)
	}
	if !c.Expired(now.Add(time.Minute)) || c.Expired(now) {
		t.Fatal("Expired disagrees with Parse")
	}
	if _, err := New(c.Owner, c.Network, 0, now); err == nil {
		t.Fatal("zero ttl accepted")
	}
	if _, err := New(c.Owner, c.Network, 200*365*24*time.Hour, now); err == nil {
		t.Fatal("expiry beyond 32 bits accepted")
	}
}

func TestCodeChecksum(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, _ := newCode(t, TTL, now)
	s := c.String()
	// Every single-character substitution in the body must be caught.
	body := s[len(Prefix):]
	const alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"
	for i := range body {
		for _, r := range alphabet {
			if byte(r) == body[i] {
				continue
			}
			bad := Prefix + body[:i] + string(r) + body[i+1:]
			if got, err := Parse(bad, now); err == nil {
				t.Fatalf("substitution at %d accepted: %+v", i, got)
			}
		}
	}
	for _, bad := range []string{"", Prefix, "lanfolk1abc", s[:len(s)-1], s + "y", strings.Replace(s, Prefix, "lanfolk-i2", 1)} {
		if _, err := Parse(bad, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestDerivation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a, _ := newCode(t, TTL, now)
	b := a
	if a.Session() != b.Session() || a.Key() != b.Key() {
		t.Fatal("derivation is not deterministic")
	}
	if a.Session() == [16]byte{} || a.Key() == [32]byte{} {
		t.Fatal("empty derivation")
	}
	// A new token, another network and another owner each give new values.
	c := a
	c.Token[0] ^= 1
	d := a
	d.Network[0] ^= 1
	e := a
	e.Owner[0] ^= 1
	for _, o := range []Code{c, d, e} {
		if o.Session() == a.Session() || o.Key() == a.Key() {
			t.Fatal("derivation ignores an input")
		}
	}
	// Expiry does not matter: the token is the secret.
	f := a
	f.Expires = f.Expires.Add(time.Hour)
	if f.Session() != a.Session() {
		t.Fatal("expiry changed the session")
	}
	// The public session ID must not reveal the key.
	k := a.Key()
	s := a.Session()
	if bytes.Equal(k[:16], s[:]) {
		t.Fatal("session and key share bytes")
	}
}

func TestSignatures(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, owner := newCode(t, TTL, now)
	joiner, _ := identity.Generate()
	sid := c.Session()
	data := JoinRequestSigData(sid, c.Network, joiner.Public(), now.Unix())
	if !joiner.Public().Verify(data, joiner.Sign(data)) {
		t.Fatal("own signature rejected")
	}
	other := JoinRequestSigData(sid, c.Network, joiner.Public(), now.Unix()+1)
	if bytes.Equal(other, data) {
		t.Fatal("timestamp not in signed data")
	}
	acc := JoinAcceptedSigData(sid, c.Network, joiner.Public())
	if bytes.Equal(acc, JoinRequestSigData(sid, c.Network, joiner.Public(), 0)) {
		t.Fatal("request and accept signatures are not domain separated")
	}
	if !owner.Public().Verify(acc, owner.Sign(acc)) {
		t.Fatal("owner signature rejected")
	}
}
