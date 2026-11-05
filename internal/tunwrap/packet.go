package tunwrap

import (
	"encoding/binary"
	"net/netip"
)

// LanFrameProto is the IP protocol number (RFC 3692 experimental) that
// identifies a LanFrame.
const LanFrameProto = 253

// LanFrame layout: a 20 byte IPv4 header followed by a 4 byte LanFrame header
// and the complete original IPv4 packet.
const (
	ipv4HdrLen   = 20
	lanHdrLen    = 4
	lanOverhead  = ipv4HdrLen + lanHdrLen
	lanVersion   = 1
	lanFrameTTL  = 64
	protoTCP     = 6
	protoUDP     = 17
	udpHdrLen    = 8
	flagDF       = 0x4000
	flagMF       = 0x2000
	fragOffMask  = 0x1fff
	maxIPv4Total = 65535
)

// csumAdd adds b to a running ones'-complement sum. 32 bit words are summed
// into a 64 bit accumulator; the final fold makes this equivalent to summing
// 16 bit words.
func csumAdd(sum uint64, b []byte) uint64 {
	for len(b) >= 8 {
		sum += uint64(binary.BigEndian.Uint32(b)) + uint64(binary.BigEndian.Uint32(b[4:]))
		b = b[8:]
	}
	for len(b) >= 2 {
		sum += uint64(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint64(b[0]) << 8
	}
	return sum
}

func csumFold(sum uint64) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return uint16(sum)
}

// ipChecksum returns the Internet checksum of b: the complement of the
// ones'-complement sum. Over a header whose checksum field is zero it yields
// the value to store; over a valid header it yields zero.
func ipChecksum(b []byte) uint16 {
	return ^csumFold(csumAdd(0, b))
}

// udpPseudoSum returns the ones'-complement sum of the UDP pseudo header and
// the datagram.
func udpPseudoSum(src, dst [4]byte, udp []byte) uint64 {
	var sum uint64
	sum += uint64(binary.BigEndian.Uint16(src[0:])) + uint64(binary.BigEndian.Uint16(src[2:]))
	sum += uint64(binary.BigEndian.Uint16(dst[0:])) + uint64(binary.BigEndian.Uint16(dst[2:]))
	sum += protoUDP + uint64(len(udp))
	return csumAdd(sum, udp)
}

// udpChecksum returns the UDP checksum to store for the datagram udp whose
// checksum field is zero. A computed zero is transmitted as 0xffff.
func udpChecksum(src, dst [4]byte, udp []byte) uint16 {
	c := ^csumFold(udpPseudoSum(src, dst, udp))
	if c == 0 {
		return 0xffff
	}
	return c
}

// udpChecksumValid verifies a UDP checksum. A zero field means "no checksum"
// and is valid.
func udpChecksumValid(src, dst [4]byte, udp []byte) bool {
	if len(udp) < udpHdrLen {
		return false
	}
	if binary.BigEndian.Uint16(udp[6:]) == 0 {
		return true
	}
	return csumFold(udpPseudoSum(src, dst, udp)) == 0xffff
}

// checksumUpdate32 incrementally updates csum (RFC 1624) after a 4 byte field,
// aligned to 16 bits, changed from old to new.
func checksumUpdate32(csum uint16, old, new [4]byte) uint16 {
	sum := uint64(^csum)
	sum += uint64(^binary.BigEndian.Uint16(old[0:])) + uint64(^binary.BigEndian.Uint16(old[2:]))
	sum += uint64(binary.BigEndian.Uint16(new[0:])) + uint64(binary.BigEndian.Uint16(new[2:]))
	return ^csumFold(sum)
}

// ipv4Header holds the fields of an IPv4 header the wrapper needs.
type ipv4Header struct {
	ihl      int // header length in bytes
	total    int // total length in bytes
	proto    uint8
	ttl      uint8
	src, dst [4]byte
	fragOff  uint16 // fragment offset in 8 byte units
	mf       bool   // more fragments
}

func (h *ipv4Header) fragmented() bool { return h.fragOff != 0 || h.mf }

// ipVersion returns the IP version of pkt, or 0 for an empty packet.
func ipVersion(pkt []byte) int {
	if len(pkt) == 0 {
		return 0
	}
	return int(pkt[0] >> 4)
}

// parseIPv4 validates the basic IPv4 framing: version, header length and total
// length against the available bytes. It does not verify checksums.
func parseIPv4(pkt []byte) (h ipv4Header, ok bool) {
	if len(pkt) < ipv4HdrLen || pkt[0]>>4 != 4 {
		return h, false
	}
	h.ihl = int(pkt[0]&0x0f) * 4
	h.total = int(binary.BigEndian.Uint16(pkt[2:]))
	if h.ihl < ipv4HdrLen || h.ihl > len(pkt) || h.total < h.ihl || h.total > len(pkt) {
		return h, false
	}
	ff := binary.BigEndian.Uint16(pkt[6:])
	h.fragOff = ff & fragOffMask
	h.mf = ff&flagMF != 0
	h.ttl = pkt[8]
	h.proto = pkt[9]
	copy(h.src[:], pkt[12:16])
	copy(h.dst[:], pkt[16:20])
	return h, true
}

func u32(a [4]byte) uint32 { return binary.BigEndian.Uint32(a[:]) }

func isMulticast(a [4]byte) bool { return a[0]&0xf0 == 0xe0 }

// addr4 converts a to a 4 byte array. Invalid and IPv6 addresses report false.
func addr4(a netip.Addr) ([4]byte, bool) {
	a = a.Unmap()
	if !a.Is4() {
		return [4]byte{}, false
	}
	return a.As4(), true
}

// writeFrame builds a LanFrame carrying inner into dst, which must have room
// for lanOverhead+len(inner) bytes. It returns the frame length.
func writeFrame(dst []byte, self, peer [4]byte, inner []byte) int {
	n := lanOverhead + len(inner)
	dst[0] = 0x45
	dst[1] = 0
	binary.BigEndian.PutUint16(dst[2:], uint16(n))
	binary.BigEndian.PutUint16(dst[4:], 0)
	binary.BigEndian.PutUint16(dst[6:], flagDF)
	dst[8] = lanFrameTTL
	dst[9] = LanFrameProto
	dst[10], dst[11] = 0, 0
	copy(dst[12:], self[:])
	copy(dst[16:], peer[:])
	binary.BigEndian.PutUint16(dst[10:], ipChecksum(dst[:ipv4HdrLen]))
	dst[20], dst[21], dst[22], dst[23] = 'L', 'F', lanVersion, 0
	copy(dst[lanOverhead:], inner)
	return n
}

// setSource rewrites the source address of the IPv4 packet pkt (already
// parsed as h), and updates the IPv4 header checksum and, for the first
// fragment of a UDP datagram with a checksum, the UDP checksum. A zero UDP
// checksum stays zero.
func setSource(pkt []byte, h *ipv4Header, src [4]byte) {
	if h.src == src {
		return
	}
	if h.proto == protoUDP && h.fragOff == 0 && h.ihl+udpHdrLen <= h.total {
		off := h.ihl + 6
		if c := binary.BigEndian.Uint16(pkt[off:]); c != 0 {
			c = checksumUpdate32(c, h.src, src)
			if c == 0 {
				c = 0xffff
			}
			binary.BigEndian.PutUint16(pkt[off:], c)
		}
	}
	copy(pkt[12:16], src[:])
	h.src = src
	pkt[10], pkt[11] = 0, 0
	binary.BigEndian.PutUint16(pkt[10:], ipChecksum(pkt[:h.ihl]))
}

// recomputeUDPChecksum computes the UDP checksum of an unfragmented datagram
// (parsed as h) from scratch. A zero field ("no checksum") stays zero.
//
// It is needed for packets captured from the loopback path: Windows hands
// over the copy of a locally sent datagram before checksum offload has run,
// so the field holds only the pseudo-header sum, and an incremental update
// (setSource) would keep it wrong (D-034).
func recomputeUDPChecksum(pkt []byte, h *ipv4Header) {
	if h.proto != protoUDP || h.fragmented() || h.ihl+udpHdrLen > h.total {
		return
	}
	udp := pkt[h.ihl:h.total]
	if binary.BigEndian.Uint16(udp[6:]) == 0 {
		return
	}
	udp[6], udp[7] = 0, 0
	binary.BigEndian.PutUint16(udp[6:], udpChecksum(h.src, h.dst, udp))
}

// BuildUDP4 returns a valid IPv4 UDP packet (DF set, header and UDP checksums
// filled in). It returns nil if the payload does not fit into one packet.
func BuildUDP4(src, dst netip.AddrPort, ttl uint8, payload []byte) []byte {
	total := ipv4HdrLen + udpHdrLen + len(payload)
	if total > maxIPv4Total {
		return nil
	}
	s, d := src.Addr().As4(), dst.Addr().As4()
	p := make([]byte, total)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(total))
	binary.BigEndian.PutUint16(p[6:], flagDF)
	p[8] = ttl
	p[9] = protoUDP
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[10:], ipChecksum(p[:ipv4HdrLen]))
	udp := p[ipv4HdrLen:]
	binary.BigEndian.PutUint16(udp[0:], src.Port())
	binary.BigEndian.PutUint16(udp[2:], dst.Port())
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	copy(udp[udpHdrLen:], payload)
	binary.BigEndian.PutUint16(udp[6:], udpChecksum(s, d, udp))
	return p
}

// ParseUDP4 parses an IPv4 UDP packet and returns its endpoints and payload
// (a sub-slice of pkt). It reports false for anything else and for packets
// with an invalid IPv4 header checksum or UDP checksum (a zero UDP checksum is
// accepted).
func ParseUDP4(pkt []byte) (src, dst netip.AddrPort, payload []byte, ok bool) {
	h, ok := parseIPv4(pkt)
	if !ok || h.proto != protoUDP || h.fragmented() || ipChecksum(pkt[:h.ihl]) != 0 {
		return src, dst, nil, false
	}
	udp := pkt[h.ihl:h.total]
	if len(udp) < udpHdrLen {
		return src, dst, nil, false
	}
	ulen := int(binary.BigEndian.Uint16(udp[4:]))
	if ulen < udpHdrLen || ulen > len(udp) {
		return src, dst, nil, false
	}
	udp = udp[:ulen]
	if !udpChecksumValid(h.src, h.dst, udp) {
		return src, dst, nil, false
	}
	src = netip.AddrPortFrom(netip.AddrFrom4(h.src), binary.BigEndian.Uint16(udp[0:]))
	dst = netip.AddrPortFrom(netip.AddrFrom4(h.dst), binary.BigEndian.Uint16(udp[2:]))
	return src, dst, udp[udpHdrLen:], true
}
