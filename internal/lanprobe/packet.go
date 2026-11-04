package lanprobe

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"sync/atomic"
)

// Checksum returns the Internet checksum (RFC 1071) of b: the ones'
// complement of the ones' complement sum of its 16-bit big-endian words. A
// buffer that already contains a correct checksum field sums to zero.
func Checksum(b []byte) uint16 {
	return finishChecksum(sumWords(0, b))
}

func sumWords(sum uint32, b []byte) uint32 {
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return sum
}

func finishChecksum(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

const (
	ipv4HeaderLen = 20
	udpHeaderLen  = 8
	protoUDP      = 17
)

var ipID atomic.Uint32

// BuildIPv4UDP builds a complete IPv4 packet carrying one UDP datagram, with
// valid IPv4 header and UDP checksums, as written to a Layer 3 device such as
// Wintun. The TTL is 1 so that a stray packet never leaves the link.
func BuildIPv4UDP(src, dst netip.Addr, srcPort, dstPort uint16, payload []byte) []byte {
	return buildIPv4UDP(uint16(ipID.Add(1)), src, dst, srcPort, dstPort, payload)
}

func buildIPv4UDP(id uint16, src, dst netip.Addr, srcPort, dstPort uint16, payload []byte) []byte {
	total := ipv4HeaderLen + udpHeaderLen + len(payload)
	p := make([]byte, total)
	p[0] = 0x45 // version 4, header length 5 words
	binary.BigEndian.PutUint16(p[2:], uint16(total))
	binary.BigEndian.PutUint16(p[4:], id)
	binary.BigEndian.PutUint16(p[6:], 0x4000) // don't fragment
	p[8] = 1                                  // TTL
	p[9] = protoUDP
	s4, d4 := src.As4(), dst.As4()
	copy(p[12:16], s4[:])
	copy(p[16:20], d4[:])
	binary.BigEndian.PutUint16(p[10:], Checksum(p[:ipv4HeaderLen]))

	u := p[ipv4HeaderLen:]
	binary.BigEndian.PutUint16(u[0:], srcPort)
	binary.BigEndian.PutUint16(u[2:], dstPort)
	binary.BigEndian.PutUint16(u[4:], uint16(udpHeaderLen+len(payload)))
	copy(u[udpHeaderLen:], payload)
	c := udpChecksum(s4, d4, u)
	if c == 0 {
		c = 0xffff // zero means "no checksum" in UDP
	}
	binary.BigEndian.PutUint16(u[6:], c)
	return p
}

// udpChecksum computes the UDP checksum of datagram (header with a zero
// checksum field, plus payload) including the IPv4 pseudo header.
func udpChecksum(src, dst [4]byte, datagram []byte) uint16 {
	var ph [12]byte
	copy(ph[0:4], src[:])
	copy(ph[4:8], dst[:])
	ph[9] = protoUDP
	binary.BigEndian.PutUint16(ph[10:], uint16(len(datagram)))
	sum := sumWords(0, ph[:])
	// A datagram of odd length is padded with a zero byte, which sumWords does.
	sum = sumWords(sum, datagram)
	return finishChecksum(sum)
}

// UDPPacket is a decoded IPv4 UDP packet.
type UDPPacket struct {
	Src, Dst         netip.Addr
	SrcPort, DstPort uint16
	Payload          []byte
	IPChecksumOK     bool
	UDPChecksumOK    bool // true when the checksum field is zero (not used)
}

// ParseIPv4UDP decodes an IPv4 packet and returns its UDP datagram. It fails
// for anything that is not an unfragmented IPv4 UDP packet.
func ParseIPv4UDP(p []byte) (UDPPacket, error) {
	var u UDPPacket
	if len(p) < ipv4HeaderLen || p[0]>>4 != 4 {
		return u, errors.New("not an IPv4 packet")
	}
	ihl := int(p[0]&0x0f) * 4
	if ihl < ipv4HeaderLen || len(p) < ihl {
		return u, errors.New("bad IPv4 header length")
	}
	total := int(binary.BigEndian.Uint16(p[2:]))
	if total < ihl+udpHeaderLen || total > len(p) {
		return u, errors.New("bad IPv4 total length")
	}
	if p[9] != protoUDP {
		return u, errors.New("not UDP")
	}
	if binary.BigEndian.Uint16(p[6:])&0x3fff != 0 {
		return u, errors.New("fragmented packet")
	}
	p = p[:total]
	u.Src = netip.AddrFrom4([4]byte(p[12:16]))
	u.Dst = netip.AddrFrom4([4]byte(p[16:20]))
	u.IPChecksumOK = Checksum(p[:ihl]) == 0
	d := p[ihl:]
	u.SrcPort = binary.BigEndian.Uint16(d[0:])
	u.DstPort = binary.BigEndian.Uint16(d[2:])
	ulen := int(binary.BigEndian.Uint16(d[4:]))
	if ulen < udpHeaderLen || ulen > len(d) {
		return u, errors.New("bad UDP length")
	}
	d = d[:ulen]
	u.Payload = d[udpHeaderLen:]
	if binary.BigEndian.Uint16(d[6:]) == 0 {
		u.UDPChecksumOK = true
	} else {
		var ph [12]byte
		copy(ph[0:4], p[12:16])
		copy(ph[4:8], p[16:20])
		ph[9] = protoUDP
		binary.BigEndian.PutUint16(ph[10:], uint16(len(d)))
		u.UDPChecksumOK = finishChecksum(sumWords(sumWords(0, ph[:]), d)) == 0
	}
	return u, nil
}
