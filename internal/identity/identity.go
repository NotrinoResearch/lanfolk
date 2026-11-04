// Package identity implements Lanfolk identities: device-generated Ed25519
// keys, friend codes and the per-pair secrets that two peers derive from
// each other's keys.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/edwards25519"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// FriendCodePrefix starts every friend code.
const FriendCodePrefix = "lanfolk1"

// zbase32 is the human-oriented base32 alphabet from the z-base-32 spec.
var zbase32 = base32.NewEncoding("ybndrfg8ejkmcpqxot1uwisza345h769").WithPadding(base32.NoPadding)

const friendCodeChecksumLen = 4

// PublicKey is an Ed25519 public key. It is the only identifier of a user.
type PublicKey [32]byte

// KeyID is a short, stable identifier derived from a public key. It is used
// in member relay headers where the full key would waste bytes.
type KeyID [8]byte

// ParsePublicKeyHex parses a 64-character hex public key.
func ParsePublicKeyHex(s string) (PublicKey, error) {
	var pk PublicKey
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(pk) {
		return pk, errors.New("invalid public key hex")
	}
	copy(pk[:], b)
	return pk, nil
}

// Hex returns the full hex form of the key.
func (p PublicKey) Hex() string { return hex.EncodeToString(p[:]) }

// String returns a short form suitable for logs.
func (p PublicKey) String() string { return hex.EncodeToString(p[:4]) }

// IsZero reports whether the key is unset.
func (p PublicKey) IsZero() bool { return p == PublicKey{} }

// Less orders keys bytewise.
func (p PublicKey) Less(o PublicKey) bool { return bytes.Compare(p[:], o[:]) < 0 }

// Verify checks an Ed25519 signature made by this key.
func (p PublicKey) Verify(msg, sig []byte) bool {
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(p[:]), msg, sig)
}

// X25519 converts the Ed25519 public key to its Montgomery (X25519) form.
// WireGuard uses this form as the peer's static public key.
func (p PublicKey) X25519() ([32]byte, error) {
	var out [32]byte
	pt, err := new(edwards25519.Point).SetBytes(p[:])
	if err != nil {
		return out, fmt.Errorf("invalid public key: %w", err)
	}
	copy(out[:], pt.BytesMontgomery())
	return out, nil
}

// KeyID returns the short key identifier.
func (p PublicKey) KeyID() KeyID {
	h := sha256.New()
	h.Write([]byte("lanfolk/v1/keyid"))
	h.Write(p[:])
	var id KeyID
	copy(id[:], h.Sum(nil))
	return id
}

func (id KeyID) String() string { return hex.EncodeToString(id[:]) }

// FriendCode returns the text form of the key: the prefix followed by the
// z-base32 encoding of the key and a 4-byte checksum that catches typos.
func (p PublicKey) FriendCode() string {
	buf := make([]byte, 0, 32+friendCodeChecksumLen)
	buf = append(buf, p[:]...)
	buf = append(buf, friendCodeChecksum(p)...)
	return FriendCodePrefix + zbase32.EncodeToString(buf)
}

func friendCodeChecksum(p PublicKey) []byte {
	h := sha256.New()
	h.Write([]byte(FriendCodePrefix))
	h.Write(p[:])
	return h.Sum(nil)[:friendCodeChecksumLen]
}

// ErrInvalidFriendCode is returned for malformed or corrupted codes.
var ErrInvalidFriendCode = errors.New("invalid friend code")

// ParseFriendCode parses a friend code. Spaces, dashes and letter case are
// ignored so that codes survive being pasted through messaging apps.
func ParseFriendCode(s string) (PublicKey, error) {
	var pk PublicKey
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '-':
			return -1
		}
		return r
	}, strings.ToLower(s))
	if !strings.HasPrefix(clean, FriendCodePrefix) {
		return pk, ErrInvalidFriendCode
	}
	raw, err := zbase32.DecodeString(clean[len(FriendCodePrefix):])
	if err != nil || len(raw) != 32+friendCodeChecksumLen {
		return pk, ErrInvalidFriendCode
	}
	copy(pk[:], raw[:32])
	if !bytes.Equal(raw[32:], friendCodeChecksum(pk)) {
		return pk, ErrInvalidFriendCode
	}
	if _, err := new(edwards25519.Point).SetBytes(pk[:]); err != nil {
		return pk, ErrInvalidFriendCode
	}
	return pk, nil
}

// PrivateKey is a user's Ed25519 identity key.
type PrivateKey struct {
	seed [ed25519.SeedSize]byte
	priv ed25519.PrivateKey
	pub  PublicKey
}

// Generate creates a new random identity key.
func Generate() (*PrivateKey, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, seed); err != nil {
		return nil, err
	}
	return FromSeed(seed)
}

// FromSeed reconstructs a key from its 32-byte seed.
func FromSeed(seed []byte) (*PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("invalid seed length")
	}
	k := &PrivateKey{priv: ed25519.NewKeyFromSeed(seed)}
	copy(k.seed[:], seed)
	copy(k.pub[:], k.priv.Public().(ed25519.PublicKey))
	return k, nil
}

// Public returns the public key.
func (k *PrivateKey) Public() PublicKey { return k.pub }

// Seed returns a copy of the private seed.
func (k *PrivateKey) Seed() []byte { return append([]byte(nil), k.seed[:]...) }

// Sign signs msg with Ed25519.
func (k *PrivateKey) Sign(msg []byte) []byte { return ed25519.Sign(k.priv, msg) }

// X25519Private returns the clamped X25519 scalar that corresponds to the
// Ed25519 key, as used by WireGuard and the pair key agreement.
func (k *PrivateKey) X25519Private() [32]byte {
	h := sha512.Sum512(k.seed[:])
	var s [32]byte
	copy(s[:], h[:32])
	s[0] &= 248
	s[31] &= 127
	s[31] |= 64
	return s
}

// Pair holds the secrets two peers share. Both sides derive identical values.
type Pair struct {
	Local     PublicKey
	Remote    PublicKey
	SessionID [16]byte
	secret    [32]byte
}

// DerivePair performs X25519 key agreement between the local identity and a
// remote public key and expands the result with HKDF-SHA256.
func DerivePair(local *PrivateKey, remote PublicKey) (*Pair, error) {
	if remote == local.Public() {
		return nil, errors.New("cannot pair with own key")
	}
	remoteX, err := remote.X25519()
	if err != nil {
		return nil, err
	}
	priv := local.X25519Private()
	shared, err := curve25519.X25519(priv[:], remoteX[:])
	if err != nil {
		return nil, fmt.Errorf("key agreement failed: %w", err)
	}
	lo, hi := local.Public(), remote
	if hi.Less(lo) {
		lo, hi = hi, lo
	}
	info := make([]byte, 0, 64)
	info = append(info, lo[:]...)
	info = append(info, hi[:]...)
	p := &Pair{Local: local.Public(), Remote: remote}
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, []byte("lanfolk/v1/pair"), info), p.secret[:]); err != nil {
		return nil, err
	}
	copy(p.SessionID[:], p.expand("lanfolk/v1/relay-session", nil, 16))
	return p, nil
}

func (p *Pair) expand(label string, ctx []byte, n int) []byte {
	info := append([]byte(label), ctx...)
	out := make([]byte, n)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, p.secret[:], info), out); err != nil {
		panic(err) // only fails when n is too large
	}
	return out
}

func (p *Pair) key32(label string, who PublicKey) [32]byte {
	var k [32]byte
	copy(k[:], p.expand(label, who[:], 32))
	return k
}

// ControlSendKey encrypts control messages from Local to Remote.
func (p *Pair) ControlSendKey() [32]byte { return p.key32("lanfolk/v1/control", p.Local) }

// ControlRecvKey decrypts control messages from Remote to Local.
func (p *Pair) ControlRecvKey() [32]byte { return p.key32("lanfolk/v1/control", p.Remote) }

// DHTSalt is the BEP 44 salt for records published by publisher.
func (p *Pair) DHTSalt(publisher PublicKey) []byte {
	return p.expand("lanfolk/v1/dht-salt", publisher[:], 16)
}

// DHTSigningKey is the BEP 44 signing key for records published by
// publisher. It is derived from the pair secret so that DHT records cannot be
// linked to the publisher's identity key.
func (p *Pair) DHTSigningKey(publisher PublicKey) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(p.expand("lanfolk/v1/dht-sign", publisher[:], ed25519.SeedSize))
}

// DHTKey encrypts DHT record contents published by publisher.
func (p *Pair) DHTKey(publisher PublicKey) [32]byte { return p.key32("lanfolk/v1/dht-enc", publisher) }

// ConnCodeKey encrypts time-limited connection codes created by sender.
func (p *Pair) ConnCodeKey(sender PublicKey) [32]byte {
	return p.key32("lanfolk/v1/conncode", sender)
}
