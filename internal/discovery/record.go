// Package discovery implements Lanfolk's fallback peer discovery: encrypted
// candidate records on the BitTorrent Mainline DHT (BEP 44) and time-limited
// connection codes that users can share by hand. See docs/PROTOCOL.md.
package discovery

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// recordVersion is the first plaintext byte of a DHT record.
	recordVersion byte = 1

	// MaxValueSize is the BEP 44 limit for the bencoded value.
	MaxValueSize = 1000

	// MaxRecordCandidates caps the candidates of one DHT record. 32 IPv6
	// candidates need 32*19+10+40 = 658 bytes, well below MaxValueSize, and the
	// resulting put request still fits one 1500-byte datagram.
	MaxRecordCandidates = 32

	recordHeaderLen = 1 + 8 + 1 // version + timestamp + candidate count
)

// Errors returned when opening a record.
var (
	ErrRecordInvalid = errors.New("discovery: invalid DHT record")
	ErrRecordStale   = errors.New("discovery: DHT record is too old")
)

// Record is the decrypted content of a DHT record: the candidate addresses a
// peer was reachable at when it was published.
type Record struct {
	Published  time.Time
	Candidates []netip.AddrPort
}

// SealRecord encrypts rec for publication by pair.Local. The result is the
// raw BEP 44 value (before bencoding). Candidates beyond MaxRecordCandidates
// are dropped, so callers should list the most useful addresses first.
//
// Layout of the returned value:
//
//	nonce[24] || XChaCha20-Poly1305(plaintext, AAD = DHT salt)
//
// and of the plaintext:
//
//	version[1]=1 || unix seconds[8, big endian] || count[1] || count * wire.AppendAddr
func SealRecord(pair *identity.Pair, rec Record) ([]byte, error) {
	if rec.Published.IsZero() {
		return nil, errors.New("discovery: record has no publication time")
	}
	cands := rec.Candidates
	if len(cands) > MaxRecordCandidates {
		cands = cands[:MaxRecordCandidates]
	}
	pt := make([]byte, 0, recordHeaderLen+len(cands)*19)
	pt = append(pt, recordVersion)
	pt = binary.BigEndian.AppendUint64(pt, uint64(rec.Published.Unix()))
	pt = append(pt, byte(len(cands)))
	for _, c := range cands {
		if !c.IsValid() {
			return nil, fmt.Errorf("discovery: invalid candidate %v", c)
		}
		pt = wire.AppendAddr(pt, c)
	}
	key := pair.DHTKey(pair.Local)
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(pt)+chacha20poly1305.Overhead)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	out = aead.Seal(out, out[:chacha20poly1305.NonceSizeX], pt, pair.DHTSalt(pair.Local))
	if n := bencodedLen(len(out)); n > MaxValueSize {
		return nil, fmt.Errorf("discovery: record of %d bytes exceeds the BEP 44 limit", n)
	}
	return out, nil
}

// OpenRecord decrypts and validates a value published by pair.Remote.
// Records older than maxAge relative to now are rejected with ErrRecordStale;
// a maxAge of zero disables the check.
func OpenRecord(pair *identity.Pair, value []byte, maxAge time.Duration, now time.Time) (Record, error) {
	var rec Record
	if len(value) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead+recordHeaderLen {
		return rec, ErrRecordInvalid
	}
	key := pair.DHTKey(pair.Remote)
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return rec, err
	}
	nonce, ct := value[:chacha20poly1305.NonceSizeX], value[chacha20poly1305.NonceSizeX:]
	pt, err := aead.Open(nil, nonce, ct, pair.DHTSalt(pair.Remote))
	if err != nil {
		return rec, ErrRecordInvalid
	}
	if pt[0] != recordVersion {
		return rec, fmt.Errorf("%w: unsupported version %d", ErrRecordInvalid, pt[0])
	}
	ts := binary.BigEndian.Uint64(pt[1:9])
	if ts > 1<<40 {
		return rec, ErrRecordInvalid
	}
	count := int(pt[9])
	cands, err := parseAddrs(pt[recordHeaderLen:], count)
	if err != nil {
		return rec, ErrRecordInvalid
	}
	rec.Published = time.Unix(int64(ts), 0)
	rec.Candidates = cands
	if maxAge > 0 && now.Sub(rec.Published) > maxAge {
		return Record{}, ErrRecordStale
	}
	return rec, nil
}

// parseAddrs decodes exactly count addresses and rejects trailing bytes.
func parseAddrs(b []byte, count int) ([]netip.AddrPort, error) {
	out := make([]netip.AddrPort, 0, count)
	for i := 0; i < count; i++ {
		a, n, err := wire.ParseAddr(b)
		if err != nil {
			return nil, err
		}
		if !a.IsValid() {
			return nil, errors.New("empty address")
		}
		out = append(out, a)
		b = b[n:]
	}
	if len(b) != 0 {
		return nil, errors.New("trailing bytes")
	}
	return out, nil
}

// bencodedLen is the size of a bencoded byte string of n bytes ("n:...").
func bencodedLen(n int) int { return len(strconv.Itoa(n)) + 1 + n }
