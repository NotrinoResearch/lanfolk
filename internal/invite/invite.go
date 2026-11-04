// Package invite implements network invite codes (docs/DECISIONS.md D-025).
//
// An invite code lets a friend join a private network without exchanging
// friend codes first. The code carries the owner's key, the network ID, a
// random token and an expiry. The token is a bearer secret: from it both
// sides derive a relay session ID and a symmetric key, meet at the shared
// relay and exchange the signed JoinRequest and JoinAccepted messages
// (internal/peermsg). Everything after that is the normal friend and member
// list machinery.
package invite

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.org/x/crypto/hkdf"
)

// Prefix starts every invite code.
const Prefix = "lanfolk-i1"

// TTL is how long a new invite stays valid.
const TTL = 7 * 24 * time.Hour

// HKDF labels (docs/PROTOCOL.md, section 10.3).
const (
	hkdfSalt     = "lanfolk/v1/invite"
	labelSession = "lanfolk/v1/invite/session"
	labelKey     = "lanfolk/v1/invite/key"
)

const (
	checksumLen = 4
	rawLen      = 32 + 16 + 16 + 4 + checksumLen
)

// Same alphabet as friend codes.
var zbase32 = base32.NewEncoding("ybndrfg8ejkmcpqxot1uwisza345h769").WithPadding(base32.NoPadding)

// Errors returned by Parse and New.
var (
	ErrInvalid = errors.New("invite: invalid code")
	ErrExpired = errors.New("invite: code expired")
)

// Code is a decoded invite code.
type Code struct {
	Owner   identity.PublicKey
	Network network.ID
	Token   [16]byte
	Expires time.Time // whole seconds
}

// New creates a code for network, valid for ttl after now, with a random token.
func New(owner identity.PublicKey, nid network.ID, ttl time.Duration, now time.Time) (Code, error) {
	c := Code{Owner: owner, Network: nid}
	if ttl <= 0 {
		return c, errors.New("invite: ttl must be positive")
	}
	exp := now.Add(ttl).Unix()
	if exp <= 0 || exp > 1<<32-1 {
		return c, errors.New("invite: expiry out of range")
	}
	if _, err := rand.Read(c.Token[:]); err != nil {
		return c, err
	}
	c.Expires = time.Unix(exp, 0)
	return c, nil
}

// Expired reports whether the code is no longer valid at now.
func (c Code) Expired(now time.Time) bool { return !now.Before(c.Expires) }

func (c Code) raw() []byte {
	b := make([]byte, 0, rawLen)
	b = append(b, c.Owner[:]...)
	b = append(b, c.Network[:]...)
	b = append(b, c.Token[:]...)
	b = binary.BigEndian.AppendUint32(b, uint32(c.Expires.Unix()))
	return append(b, checksum(b)...)
}

func checksum(payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte(Prefix))
	h.Write(payload)
	return h.Sum(nil)[:checksumLen]
}

// String returns the text form: the prefix followed by the z-base32 encoding
// of owner key, network ID, token, expiry and a checksum that catches typos.
func (c Code) String() string { return Prefix + zbase32.EncodeToString(c.raw()) }

// Parse decodes a code. Spaces, dashes and letter case are ignored so that
// codes survive being pasted through messaging apps. It returns ErrInvalid
// for malformed or corrupted codes and ErrExpired (with the decoded code)
// for a well-formed code that is no longer valid at now.
func Parse(s string, now time.Time) (Code, error) {
	var c Code
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '-':
			return -1
		}
		return r
	}, strings.ToLower(s))
	prefix := strings.ReplaceAll(Prefix, "-", "")
	if !strings.HasPrefix(clean, prefix) {
		return c, ErrInvalid
	}
	body := clean[len(prefix):]
	raw, err := zbase32.DecodeString(body)
	// The last character has spare bits; require the canonical encoding so
	// that every code has exactly one spelling.
	if err != nil || len(raw) != rawLen || zbase32.EncodeToString(raw) != body {
		return c, ErrInvalid
	}
	if !bytes.Equal(raw[rawLen-checksumLen:], checksum(raw[:rawLen-checksumLen])) {
		return c, ErrInvalid
	}
	copy(c.Owner[:], raw[0:32])
	copy(c.Network[:], raw[32:48])
	copy(c.Token[:], raw[48:64])
	c.Expires = time.Unix(int64(binary.BigEndian.Uint32(raw[64:68])), 0)
	if c.Owner.IsZero() {
		return Code{}, ErrInvalid
	}
	if c.Expired(now) {
		return c, ErrExpired
	}
	return c, nil
}

func (c Code) expand(label string, n int) []byte {
	info := append([]byte(label), c.Owner[:]...)
	info = append(info, c.Network[:]...)
	out := make([]byte, n)
	if _, err := io.ReadFull(hkdf.New(sha256.New, c.Token[:], []byte(hkdfSalt), info), out); err != nil {
		panic(err) // only fails when n is too large
	}
	return out
}

// Session returns the relay session ID where owner and joiner meet. The relay
// sees it but cannot link it to an identity or to the key.
func (c Code) Session() wire.SessionID {
	var s wire.SessionID
	copy(s[:], c.expand(labelSession, len(s)))
	return s
}

// Key returns the symmetric key that seals the control frames of the invite
// session in both directions.
func (c Code) Key() [32]byte {
	var k [32]byte
	copy(k[:], c.expand(labelKey, len(k)))
	return k
}

// Domain separators for the two signatures.
var (
	joinRequestDomain  = []byte("lanfolk/v1/join-request\x00")
	joinAcceptedDomain = []byte("lanfolk/v1/join-accepted\x00")
)

// JoinRequestSigData is what a joiner signs: invite session, network, the
// joiner's own key and the request time in unix seconds.
func JoinRequestSigData(session wire.SessionID, nid network.ID, joiner identity.PublicKey, unix int64) []byte {
	b := append([]byte(nil), joinRequestDomain...)
	b = append(b, session[:]...)
	b = append(b, nid[:]...)
	b = append(b, joiner[:]...)
	return binary.BigEndian.AppendUint64(b, uint64(unix))
}

// JoinAcceptedSigData is what the owner signs to accept one joiner: invite
// session, network and the joiner's key.
func JoinAcceptedSigData(session wire.SessionID, nid network.ID, joiner identity.PublicKey) []byte {
	b := append([]byte(nil), joinAcceptedDomain...)
	b = append(b, session[:]...)
	b = append(b, nid[:]...)
	return append(b, joiner[:]...)
}
