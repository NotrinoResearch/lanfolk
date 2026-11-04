package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"hash/maphash"
	"net/netip"
	"sync"

	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// limitKey is the address that per-IP limits count against. An IPv6 user
// normally controls a whole /64, so IPv6 addresses are counted per /64;
// otherwise one host could use a fresh address for every request.
func limitKey(a netip.Addr) netip.Addr {
	if a.Is6() && !a.Is4In6() {
		p, err := a.Prefix(64)
		if err == nil {
			return p.Addr()
		}
	}
	return a
}

// ipBuckets is a token bucket per source IP, stored in a fixed array indexed
// by a keyed hash of the address. Memory never grows with the number of
// sources, so a flood from many (spoofed) addresses cannot push out or lock
// out other clients; two addresses that share a slot share its budget, which
// only makes the limit stricter for them. The hash key is random per process,
// so nobody can aim at a victim's slot from the outside.
type ipBuckets struct {
	seed  maphash.Seed
	rate  float64 // units per second
	burst float64 // bucket size in units
	mask  uint64
	slots []ipSlot
}

type ipSlot struct {
	mu     sync.Mutex
	tokens float64
	last   int64 // unix nanoseconds; 0 = never used (full bucket)
}

// ipBucketSlots is the number of slots: about 1.5 MB of memory per limiter,
// and few collisions for tens of thousands of active sources.
const ipBucketSlots = 1 << 16

func newIPBuckets(rate, burst float64) *ipBuckets {
	return &ipBuckets{seed: maphash.MakeSeed(), rate: rate, burst: burst,
		mask: ipBucketSlots - 1, slots: make([]ipSlot, ipBucketSlots)}
}

func (l *ipBuckets) slot(ip netip.Addr) *ipSlot {
	a := limitKey(ip).As16()
	return &l.slots[maphash.Bytes(l.seed, a[:])&l.mask]
}

// allow takes n units from ip's bucket at time now (unix nanoseconds).
func (l *ipBuckets) allow(ip netip.Addr, n float64, now int64) bool {
	sl := l.slot(ip)
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.last == 0 {
		sl.tokens, sl.last = l.burst, now
	}
	if now > sl.last {
		sl.tokens += float64(now-sl.last) / 1e9 * l.rate
		if sl.tokens > l.burst {
			sl.tokens = l.burst
		}
		sl.last = now
	}
	if sl.tokens < n {
		return false
	}
	sl.tokens -= n
	return true
}

// cookieWindow is the time slot of a Register cookie. A cookie is accepted in
// its own window and the next one, so it stays valid for 30 to 60 seconds.
const cookieWindowSec = 30

// cookieMaker computes the stateless Register cookies (decision R5 in the
// relay hardening): a keyed SHA-256 over a fixed-length input, truncated to
// 16 bytes. Because the input has a fixed length, the keyed hash cannot be
// extended, so it is a sound MAC here, and it needs no allocation.
type cookieMaker struct {
	secret [32]byte
}

func newCookieMaker() *cookieMaker {
	c := &cookieMaker{}
	rand.Read(c.secret[:])
	return c
}

func (c *cookieMaker) make(src netip.AddrPort, sid wire.SessionID, window int64) wire.Cookie {
	var in [32 + 16 + 2 + 16 + 8]byte
	copy(in[:32], c.secret[:])
	a := src.Addr().Unmap().As16()
	copy(in[32:48], a[:])
	binary.BigEndian.PutUint16(in[48:50], src.Port())
	copy(in[50:66], sid[:])
	binary.BigEndian.PutUint64(in[66:74], uint64(window))
	sum := sha256.Sum256(in[:])
	var out wire.Cookie
	copy(out[:], sum[:wire.CookieSize])
	return out
}

// current returns the cookie for src and sid at time now (unix seconds).
func (c *cookieMaker) current(src netip.AddrPort, sid wire.SessionID, nowSec int64) wire.Cookie {
	return c.make(src, sid, nowSec/cookieWindowSec)
}

// valid reports whether got is the cookie of src and sid for the current or
// the previous window.
func (c *cookieMaker) valid(src netip.AddrPort, sid wire.SessionID, got wire.Cookie, nowSec int64) bool {
	w := nowSec / cookieWindowSec
	for _, win := range [2]int64{w, w - 1} {
		want := c.make(src, sid, win)
		if subtle.ConstantTimeCompare(want[:], got[:]) == 1 {
			return true
		}
	}
	return false
}
