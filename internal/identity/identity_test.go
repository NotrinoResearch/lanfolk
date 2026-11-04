package identity

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

func TestFriendCodeRoundTrip(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	code := k.Public().FriendCode()
	if !strings.HasPrefix(code, FriendCodePrefix) {
		t.Fatalf("code %q lacks prefix", code)
	}
	got, err := ParseFriendCode(code)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != k.Public() {
		t.Fatal("round trip mismatch")
	}
	// Whitespace, dashes and upper case are tolerated when pasting.
	messy := "  " + strings.ToUpper(code[:20]) + "-" + code[20:] + "\n"
	got, err = ParseFriendCode(messy)
	if err != nil || got != k.Public() {
		t.Fatalf("messy parse failed: %v", err)
	}
}

func TestFriendCodeRejectsTypos(t *testing.T) {
	k, _ := Generate()
	code := k.Public().FriendCode()
	body := []byte(code)
	// Flip one character of the encoded body to another alphabet letter.
	i := len(FriendCodePrefix) + 5
	if body[i] == 'y' {
		body[i] = 'b'
	} else {
		body[i] = 'y'
	}
	if _, err := ParseFriendCode(string(body)); err == nil {
		t.Fatal("corrupted code accepted")
	}
	if _, err := ParseFriendCode("lanfolk2" + code[len(FriendCodePrefix):]); err == nil {
		t.Fatal("wrong prefix accepted")
	}
	if _, err := ParseFriendCode(code[:len(code)-3]); err == nil {
		t.Fatal("truncated code accepted")
	}
}

func TestX25519ConversionMatchesEd25519(t *testing.T) {
	k, _ := Generate()
	priv := k.X25519Private()
	pubFromPriv, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	pubFromEd, err := k.Public().X25519()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pubFromPriv, pubFromEd[:]) {
		t.Fatal("converted public key does not match converted private key")
	}
}

func TestPairIsSymmetric(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	pa, err := DerivePair(a, b.Public())
	if err != nil {
		t.Fatal(err)
	}
	pb, err := DerivePair(b, a.Public())
	if err != nil {
		t.Fatal(err)
	}
	if pa.SessionID != pb.SessionID {
		t.Fatal("session IDs differ")
	}
	if pa.ControlSendKey() != pb.ControlRecvKey() || pa.ControlRecvKey() != pb.ControlSendKey() {
		t.Fatal("control keys are not mirrored")
	}
	if pa.ControlSendKey() == pa.ControlRecvKey() {
		t.Fatal("directional keys must differ")
	}
	if !bytes.Equal(pa.DHTSalt(a.Public()), pb.DHTSalt(a.Public())) {
		t.Fatal("DHT salt differs")
	}
	if bytes.Equal(pa.DHTSalt(a.Public()), pa.DHTSalt(b.Public())) {
		t.Fatal("DHT salt must depend on the publisher")
	}
	if !bytes.Equal(pa.DHTSigningKey(b.Public()), pb.DHTSigningKey(b.Public())) {
		t.Fatal("DHT signing keys differ")
	}
	if pa.DHTKey(a.Public()) != pb.DHTKey(a.Public()) {
		t.Fatal("DHT encryption keys differ")
	}
	if pa.ConnCodeKey(a.Public()) != pb.ConnCodeKey(a.Public()) {
		t.Fatal("connection code keys differ")
	}
}

func TestPairDiffersPerPeer(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	c, _ := Generate()
	ab, _ := DerivePair(a, b.Public())
	ac, _ := DerivePair(a, c.Public())
	if ab.SessionID == ac.SessionID {
		t.Fatal("different peers share a session ID")
	}
}

func TestPairRejectsSelf(t *testing.T) {
	a, _ := Generate()
	if _, err := DerivePair(a, a.Public()); err == nil {
		t.Fatal("pair with self accepted")
	}
}

func TestSignVerify(t *testing.T) {
	k, _ := Generate()
	msg := []byte("member list")
	sig := k.Sign(msg)
	if !k.Public().Verify(msg, sig) {
		t.Fatal("valid signature rejected")
	}
	msg[0] ^= 1
	if k.Public().Verify(msg, sig) {
		t.Fatal("invalid signature accepted")
	}
}

func TestSeedRoundTrip(t *testing.T) {
	k, _ := Generate()
	k2, err := FromSeed(k.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if k2.Public() != k.Public() {
		t.Fatal("seed round trip mismatch")
	}
}

func TestBackupRoundTrip(t *testing.T) {
	k, _ := Generate()
	backup, err := ExportBackup(k, "Ahmet", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	k2, nick, err := ImportBackup(backup, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if k2.Public() != k.Public() || nick != "Ahmet" {
		t.Fatal("backup round trip mismatch")
	}
	if _, _, err := ImportBackup(backup, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestProtectorRoundTrip(t *testing.T) {
	k, _ := Generate()
	p := DefaultProtector()
	blob, err := p.Protect(k.Seed())
	if err != nil {
		t.Fatal(err)
	}
	seed, err := p.Unprotect(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seed, k.Seed()) {
		t.Fatal("protector round trip mismatch")
	}
}

func TestKeyIDStable(t *testing.T) {
	k, _ := Generate()
	if k.Public().KeyID() != k.Public().KeyID() {
		t.Fatal("key ID not deterministic")
	}
	k2, _ := Generate()
	if k.Public().KeyID() == k2.Public().KeyID() {
		t.Fatal("key ID collision")
	}
}

func TestDHTEpochRotation(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	pa, _ := DerivePair(a, b.Public())
	pb, _ := DerivePair(b, a.Public())
	day := DHTEpoch(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if day != DHTEpoch(time.Date(2026, 11, 4, 23, 59, 0, 0, time.UTC)) || day == DHTEpoch(time.Date(2026, 11, 5, 0, 0, 1, 0, time.UTC)) {
		t.Fatal("epoch is not a UTC day")
	}
	ea, eb := pa.ForDHTEpoch(day), pb.ForDHTEpoch(day)
	if !bytes.Equal(ea.DHTSalt(a.Public()), eb.DHTSalt(a.Public())) || ea.DHTKey(a.Public()) != eb.DHTKey(a.Public()) ||
		!bytes.Equal(ea.DHTSigningKey(a.Public()), eb.DHTSigningKey(a.Public())) {
		t.Fatal("epoch keys differ between the peers")
	}
	next := pa.ForDHTEpoch(day + 1)
	if bytes.Equal(ea.DHTSalt(a.Public()), next.DHTSalt(a.Public())) ||
		bytes.Equal(ea.DHTSigningKey(a.Public()), next.DHTSigningKey(a.Public())) ||
		ea.DHTKey(a.Public()) == next.DHTKey(a.Public()) {
		t.Fatal("DHT keys do not rotate between days")
	}
	if bytes.Equal(ea.DHTSalt(a.Public()), pa.DHTSalt(a.Public())) {
		t.Fatal("epoch keys equal the base keys")
	}
}
