package discovery

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.org/x/crypto/chacha20poly1305"
)

// ConnCodePrefix starts every connection code.
const ConnCodePrefix = "lanfolk-c1"

// MaxConnCodeCandidates caps the candidates in a code to keep it short
// enough to paste into a chat.
const MaxConnCodeCandidates = 6

// Same alphabet as friend codes.
var zbase32 = base32.NewEncoding("ybndrfg8ejkmcpqxot1uwisza345h769").WithPadding(base32.NoPadding)

const (
	connCodeAADLen    = 8 + 4                                        // key ID + expiry
	connCodeHeaderLen = connCodeAADLen + chacha20poly1305.NonceSizeX // + nonce
)

// Errors returned by ParseConnCode.
var (
	ErrConnCodeInvalid = errors.New("discovery: invalid connection code")
	ErrConnCodeExpired = errors.New("discovery: connection code expired")
	ErrConnCodeUnknown = errors.New("discovery: connection code is from an unknown peer")
)

// CreateConnCode builds a connection code for pair.Remote that is valid for
// ttl after now. At most MaxConnCodeCandidates candidates are included.
//
// Binary layout, before z-base32 encoding:
//
//	sender key ID[8] || expiry unix seconds[4, big endian] || nonce[24] ||
//	XChaCha20-Poly1305(count[1] || count * wire.AppendAddr,
//	                   key = pair.ConnCodeKey(sender), AAD = key ID || expiry)
//
// The text form is ConnCodePrefix followed by the encoding.
func CreateConnCode(pair *identity.Pair, candidates []netip.AddrPort, ttl time.Duration, now time.Time) (string, error) {
	if ttl <= 0 {
		return "", errors.New("discovery: connection code ttl must be positive")
	}
	exp := now.Add(ttl).Unix()
	if exp <= 0 || exp > 1<<32-1 {
		return "", errors.New("discovery: connection code expiry out of range")
	}
	if len(candidates) > MaxConnCodeCandidates {
		candidates = candidates[:MaxConnCodeCandidates]
	}
	if len(candidates) == 0 {
		return "", errors.New("discovery: connection code needs at least one candidate")
	}
	pt := []byte{byte(len(candidates))}
	for _, c := range candidates {
		if !c.IsValid() {
			return "", errors.New("discovery: invalid candidate")
		}
		pt = wire.AppendAddr(pt, c)
	}
	id := pair.Local.KeyID()
	buf := make([]byte, 0, connCodeHeaderLen+len(pt)+chacha20poly1305.Overhead)
	buf = append(buf, id[:]...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(exp))
	buf = append(buf, make([]byte, chacha20poly1305.NonceSizeX)...)
	if _, err := rand.Read(buf[connCodeAADLen:connCodeHeaderLen]); err != nil {
		return "", err
	}
	key := pair.ConnCodeKey(pair.Local)
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return "", err
	}
	buf = aead.Seal(buf, buf[connCodeAADLen:connCodeHeaderLen], pt, buf[:connCodeAADLen])
	return ConnCodePrefix + zbase32.EncodeToString(buf), nil
}

// ParseConnCode decodes a connection code. lookup maps the sender's key ID
// to the local pair with that peer, or returns nil if the peer is unknown.
// Spaces, dashes and letter case are ignored.
func ParseConnCode(code string, lookup func(identity.KeyID) *identity.Pair, now time.Time) (sender identity.PublicKey, candidates []netip.AddrPort, expires time.Time, err error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '-':
			return -1
		}
		return r
	}, strings.ToLower(code))
	prefix := strings.ReplaceAll(ConnCodePrefix, "-", "")
	if !strings.HasPrefix(clean, prefix) {
		err = ErrConnCodeInvalid
		return
	}
	raw, derr := zbase32.DecodeString(clean[len(prefix):])
	if derr != nil || len(raw) < connCodeHeaderLen+1+chacha20poly1305.Overhead {
		err = ErrConnCodeInvalid
		return
	}
	var id identity.KeyID
	copy(id[:], raw[:8])
	pair := lookup(id)
	if pair == nil {
		err = ErrConnCodeUnknown
		return
	}
	if pair.Remote.KeyID() != id {
		err = ErrConnCodeInvalid
		return
	}
	key := pair.ConnCodeKey(pair.Remote)
	aead, aerr := chacha20poly1305.NewX(key[:])
	if aerr != nil {
		err = aerr
		return
	}
	pt, oerr := aead.Open(nil, raw[connCodeAADLen:connCodeHeaderLen], raw[connCodeHeaderLen:], raw[:connCodeAADLen])
	if oerr != nil {
		err = ErrConnCodeInvalid
		return
	}
	exp := time.Unix(int64(binary.BigEndian.Uint32(raw[8:12])), 0)
	if !now.Before(exp) {
		err = ErrConnCodeExpired
		return
	}
	cands, perr := parseAddrs(pt[1:], int(pt[0]))
	if perr != nil || len(cands) == 0 {
		err = ErrConnCodeInvalid
		return
	}
	return pair.Remote, cands, exp, nil
}
