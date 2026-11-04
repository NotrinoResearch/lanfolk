package tunwrap

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"net/netip"
	"testing"
)

func TestIPChecksumKnownVector(t *testing.T) {
	// The classic example header; the correct checksum is 0xb861.
	hdr := []byte{
		0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00, 0x40, 0x11, 0x00, 0x00,
		0xc0, 0xa8, 0x00, 0x01, 0xc0, 0xa8, 0x00, 0xc7,
	}
	if got := ipChecksum(hdr); got != 0xb861 {
		t.Fatalf("ipChecksum = %#04x, want 0xb861", got)
	}
	binary.BigEndian.PutUint16(hdr[10:], 0xb861)
	if got := ipChecksum(hdr); got != 0 {
		t.Fatalf("checksum over a valid header = %#04x, want 0", got)
	}
}

func TestIPChecksumMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 200; n++ {
		b := make([]byte, rng.Intn(1500))
		rng.Read(b)
		if got, want := ipChecksum(b), refChecksum(b); got != want {
			t.Fatalf("len %d: ipChecksum = %#04x, ref %#04x", len(b), got, want)
		}
	}
}

func TestUDPChecksumKnownVectors(t *testing.T) {
	// Vectors computed independently with a Python implementation.
	tests := []struct {
		src, dst   netip.Addr
		sport, dp  uint16
		payload    string
		want       uint16
		wantHeader uint16
	}{
		{addrSelf, addrGlobal, 4000, 4001, "hello lanfolk", 0xdd0d, 0x3076},
		{addrB, addrMcast, 50000, 7777, "ab", 0x7529, 0},
	}
	for _, tc := range tests {
		udp := make([]byte, 8+len(tc.payload))
		binary.BigEndian.PutUint16(udp[0:], tc.sport)
		binary.BigEndian.PutUint16(udp[2:], tc.dp)
		binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
		copy(udp[8:], tc.payload)
		if got := udpChecksum(tc.src.As4(), tc.dst.As4(), udp); got != tc.want {
			t.Errorf("udpChecksum(%v->%v) = %#04x, want %#04x", tc.src, tc.dst, got, tc.want)
		}
		if got := refUDPChecksum(tc.src, tc.dst, udp); got != tc.want {
			t.Errorf("reference checksum = %#04x, want %#04x (test vector is wrong)", got, tc.want)
		}
		if tc.wantHeader != 0 {
			pkt := BuildUDP4(netip.AddrPortFrom(tc.src, tc.sport), netip.AddrPortFrom(tc.dst, tc.dp), 64, []byte(tc.payload))
			if got := binary.BigEndian.Uint16(pkt[10:]); got != tc.wantHeader {
				t.Errorf("BuildUDP4 header checksum = %#04x, want %#04x", got, tc.wantHeader)
			}
			if got := binary.BigEndian.Uint16(pkt[26:]); got != tc.want {
				t.Errorf("BuildUDP4 UDP checksum = %#04x, want %#04x", got, tc.want)
			}
		}
	}
}

func TestChecksumUpdate32(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for n := 0; n < 500; n++ {
		udp := make([]byte, 8+rng.Intn(100))
		rng.Read(udp)
		binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
		udp[6], udp[7] = 0, 0
		var s1, d, s2 [4]byte
		rng.Read(s1[:])
		rng.Read(d[:])
		rng.Read(s2[:])
		c1 := udpChecksum(s1, d, udp)
		want := udpChecksum(s2, d, udp)
		if got := checksumUpdate32(c1, s1, s2); got != want && !(got == 0 && want == 0xffff) && !(got == 0xffff && want == 0) {
			t.Fatalf("incremental %#04x, full %#04x", got, want)
		}
	}
}

func TestBuildParseUDP4RoundTrip(t *testing.T) {
	src := netip.AddrPortFrom(addrSelf, 1234)
	dst := netip.AddrPortFrom(addrBcast, 27015)
	for _, payload := range [][]byte{nil, []byte("x"), []byte("odd"), bytes.Repeat([]byte{0xFF}, 1300)} {
		pkt := BuildUDP4(src, dst, 7, payload)
		if len(pkt) != 28+len(payload) {
			t.Fatalf("length %d", len(pkt))
		}
		if pkt[0] != 0x45 || pkt[8] != 7 || pkt[9] != 17 {
			t.Fatalf("bad header %x", pkt[:20])
		}
		if ipChecksum(pkt[:20]) != 0 {
			t.Fatalf("invalid IPv4 header checksum")
		}
		if got := refUDPChecksum(addrSelf, addrBcast, zeroCsum(pkt[20:])); got != binary.BigEndian.Uint16(pkt[26:]) {
			t.Fatalf("invalid UDP checksum")
		}
		s, d, p, ok := ParseUDP4(pkt)
		if !ok || s != src || d != dst || !bytes.Equal(p, payload) {
			t.Fatalf("ParseUDP4 = %v %v %q %v", s, d, p, ok)
		}
	}
}

func zeroCsum(udp []byte) []byte {
	c := append([]byte(nil), udp...)
	c[6], c[7] = 0, 0
	return c
}

func TestParseUDP4Rejects(t *testing.T) {
	good := udpPkt(addrSelf, addrB, 1, 2, "payload")
	if _, _, _, ok := ParseUDP4(good); !ok {
		t.Fatal("good packet rejected")
	}
	mutate := func(f func(p []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	bad := map[string][]byte{
		"empty":        nil,
		"short":        good[:19],
		"truncated":    good[:len(good)-1],
		"ipv6":         ipv6Pkt(),
		"tcp":          rawTCP(addrSelf, addrB, 1, 2),
		"bad ip csum":  mutate(func(p []byte) []byte { p[10] ^= 0xFF; return p }),
		"bad udp csum": mutate(func(p []byte) []byte { p[len(p)-1] ^= 0xFF; return p }),
		"bad udp len":  mutate(func(p []byte) []byte { p[25] = 200; return p }),
	}
	for name, p := range bad {
		if _, _, _, ok := ParseUDP4(p); ok {
			t.Errorf("%s: ParseUDP4 accepted the packet", name)
		}
	}
	// A zero UDP checksum means "none" and is valid.
	z := append([]byte(nil), good...)
	z[26], z[27] = 0, 0
	if _, _, _, ok := ParseUDP4(z); !ok {
		t.Error("zero UDP checksum rejected")
	}
}
