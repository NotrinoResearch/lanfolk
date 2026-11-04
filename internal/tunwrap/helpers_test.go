package tunwrap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/winnet"
)

var (
	testPrefix = netip.MustParsePrefix("10.77.0.0/24")
	addrSelf   = netip.MustParseAddr("10.77.0.2")
	addrB      = netip.MustParseAddr("10.77.0.3")
	addrC      = netip.MustParseAddr("10.77.0.4")
	addrD      = netip.MustParseAddr("10.77.0.5")
	addrBcast  = netip.MustParseAddr("10.77.0.255")
	addrGlobal = netip.MustParseAddr("255.255.255.255")
	addrMcast  = netip.MustParseAddr("239.255.77.77")
)

// testOffset mimics the headroom wireguard-go passes to Read and Write.
const testOffset = 16

// refChecksum is a deliberately naive Internet checksum used to cross-check the
// optimized implementation.
func refChecksum(b []byte) uint16 {
	var sum uint64
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint64(b[i])<<8 | uint64(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint64(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// refUDPChecksum computes the UDP checksum of udp (checksum field must be
// zero) from an explicitly assembled pseudo header.
func refUDPChecksum(src, dst netip.Addr, udp []byte) uint16 {
	var buf bytes.Buffer
	s, d := src.As4(), dst.As4()
	buf.Write(s[:])
	buf.Write(d[:])
	buf.Write([]byte{0, 17})
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(udp)))
	buf.Write(udp)
	c := refChecksum(buf.Bytes())
	if c == 0 {
		return 0xffff
	}
	return c
}

// rawIPv4 builds an IPv4 packet with the given protocol and body without
// relying on the package under test.
func rawIPv4(src, dst netip.Addr, proto, ttl uint8, body []byte) []byte {
	p := make([]byte, 20+len(body))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[6] = 0x40
	p[8] = ttl
	p[9] = proto
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], refChecksum(p[:20]))
	copy(p[20:], body)
	return p
}

// rawTCP builds a minimal TCP segment (no valid checksum; the wrapper must not
// care) with the given destination port.
func rawTCP(src, dst netip.Addr, sport, dport uint16) []byte {
	seg := make([]byte, 20)
	binary.BigEndian.PutUint16(seg[0:], sport)
	binary.BigEndian.PutUint16(seg[2:], dport)
	seg[12] = 5 << 4
	seg[13] = 0x02
	return rawIPv4(src, dst, 6, 64, seg)
}

func udpPkt(src, dst netip.Addr, sport, dport uint16, payload string) []byte {
	return BuildUDP4(netip.AddrPortFrom(src, sport), netip.AddrPortFrom(dst, dport), 64, []byte(payload))
}

// ipv6Pkt returns a minimal IPv6 UDP packet.
func ipv6Pkt() []byte {
	p := make([]byte, 48)
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:], 8)
	p[6] = 17
	p[7] = 64
	p[39] = 1
	p[47] = 1
	return p
}

// lanFrame builds a LanFrame independently of the package under test.
func lanFrame(src, dst netip.Addr, inner []byte) []byte {
	body := append([]byte{'L', 'F', 1, 0}, inner...)
	p := make([]byte, 20+len(body))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[6] = 0x40
	p[8] = 64
	p[9] = 253
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], refChecksum(p[:20]))
	copy(p[20:], body)
	return p
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testEnv struct {
	d     *Device
	mem   *MemTUN
	clock *fakeClock

	mu    sync.Mutex
	self  netip.Addr
	peers []netip.Addr
}

func (e *testEnv) setSelf(a netip.Addr) {
	e.mu.Lock()
	e.self = a
	e.mu.Unlock()
}

func (e *testEnv) setPeers(p ...netip.Addr) {
	e.mu.Lock()
	e.peers = p
	e.mu.Unlock()
}

func newEnv(t testing.TB, mutate func(*Config), peers ...netip.Addr) *testEnv {
	t.Helper()
	return newEnvMem(t, NewMemTUN("test0", 1380), mutate, peers...)
}

func newEnvMem(t testing.TB, mem *MemTUN, mutate func(*Config), peers ...netip.Addr) *testEnv {
	t.Helper()
	e := &testEnv{mem: mem, self: addrSelf, peers: peers, clock: &fakeClock{t: time.Unix(1_000_000, 0)}}
	cfg := Config{
		Self: func() netip.Addr {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.self
		},
		Peers: func() []netip.Addr {
			e.mu.Lock()
			defer e.mu.Unlock()
			return append([]netip.Addr(nil), e.peers...)
		},
		Blocked: winnet.IsBlocked,
		Prefix:  testPrefix,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.d = Wrap(mem, cfg)
	e.d.now = e.clock.Now
	t.Cleanup(func() { _ = e.d.Close() })
	return e
}

type readResult struct {
	pkts [][]byte
	err  error
}

// readN performs one Read with nbufs buffers and returns copies of the
// returned packets. It fails the test if Read does not return in time.
func (e *testEnv) readN(t testing.TB, nbufs int) [][]byte {
	t.Helper()
	pkts, err := e.tryRead(nbufs, 3*time.Second)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return pkts
}

func (e *testEnv) tryRead(nbufs int, timeout time.Duration) ([][]byte, error) {
	ch := make(chan readResult, 1)
	go func() {
		bufs := make([][]byte, nbufs)
		sizes := make([]int, nbufs)
		for i := range bufs {
			bufs[i] = bytes.Repeat([]byte{0xAA}, 4096)
		}
		n, err := e.d.Read(bufs, sizes, testOffset)
		var out [][]byte
		for i := 0; i < n; i++ {
			// Headroom must stay untouched.
			if !bytes.Equal(bufs[i][:testOffset], bytes.Repeat([]byte{0xAA}, testOffset)) {
				err = fmt.Errorf("headroom of buffer %d was modified", i)
			}
			out = append(out, append([]byte(nil), bufs[i][testOffset:testOffset+sizes[i]]...))
		}
		ch <- readResult{out, err}
	}()
	select {
	case r := <-ch:
		return r.pkts, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("Read did not return within %v", timeout)
	}
}

// readOne reads exactly one packet.
func (e *testEnv) readOne(t testing.TB) []byte {
	t.Helper()
	pkts := e.readN(t, 4)
	if len(pkts) != 1 {
		t.Fatalf("Read returned %d packets, want 1", len(pkts))
	}
	return pkts[0]
}

// writeIn feeds packets to Write with the wireguard headroom.
func (e *testEnv) writeIn(t testing.TB, pkts ...[]byte) {
	t.Helper()
	bufs := make([][]byte, len(pkts))
	for i, p := range pkts {
		b := bytes.Repeat([]byte{0xBB}, testOffset+len(p))
		copy(b[testOffset:], p)
		bufs[i] = b
	}
	n, err := e.d.Write(bufs, testOffset)
	if err != nil || n != len(pkts) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(pkts))
	}
}

// drainOS returns everything delivered to the OS so far.
func (e *testEnv) drainOS() [][]byte {
	var out [][]byte
	for {
		select {
		case p := <-e.mem.ToOS():
			out = append(out, p)
		default:
			return out
		}
	}
}

// checkFrame verifies every byte of a LanFrame.
func checkFrame(t testing.TB, frame []byte, src, dst netip.Addr, inner []byte) {
	t.Helper()
	want := lanFrame(src, dst, inner)
	if bytes.Equal(frame, want) {
		return
	}
	if len(frame) != len(want) {
		t.Fatalf("frame length %d, want %d\n got %x\nwant %x", len(frame), len(want), frame, want)
	}
	for i := range want {
		if frame[i] != want[i] {
			t.Fatalf("frame byte %d = %#02x, want %#02x\n got %x\nwant %x", i, frame[i], want[i], frame, want)
		}
	}
}

func mustUDP(t testing.TB, pkt []byte) (src, dst netip.AddrPort, payload []byte) {
	t.Helper()
	src, dst, payload, ok := ParseUDP4(pkt)
	if !ok {
		t.Fatalf("not a valid IPv4 UDP packet: %x", pkt)
	}
	return src, dst, payload
}
