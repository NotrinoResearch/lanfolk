// Package wire defines the packet formats that share Lanfolk's single UDP
// socket: STUN, WireGuard, shared relay frames, encrypted peer control
// frames and member relay frames. See docs/PROTOCOL.md.
package wire

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"

	"github.com/NotrinoResearch/lanfolk/internal/stun"
	"golang.org/x/crypto/chacha20poly1305"
)

// First-byte values of Lanfolk frames. WireGuard uses 1-4 and STUN uses
// 0x00-0x3F, so these never collide.
const (
	KindRelayControl  byte = 0xF0
	KindRelayData     byte = 0xF1
	KindControl       byte = 0xF2
	KindMemberForward byte = 0xF3
	KindMemberDeliver byte = 0xF4
)

// RelayVersion is the shared relay protocol version.
const RelayVersion byte = 1

// Header sizes.
const (
	RelayDataOverhead   = 1 + 16     // kind + session ID
	MemberRelayOverhead = 1 + 16 + 8 // kind + session ID + key ID
	ControlOverhead     = 1 + 16 + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	// MaxPacket is the largest datagram Lanfolk sends or accepts.
	MaxPacket = 1600
)

// SessionID identifies a pair of peers at the relay and in control frames.
type SessionID [16]byte

// Token distinguishes the two parties of a relay session.
type Token [8]byte

// Class is the result of demultiplexing a datagram.
type Class int

const (
	ClassUnknown Class = iota
	ClassSTUN
	ClassWireGuard
	ClassRelayControl
	ClassRelayData
	ClassControl
	ClassMemberForward
	ClassMemberDeliver
)

func (c Class) String() string {
	return [...]string{"unknown", "stun", "wireguard", "relay-control", "relay-data", "control", "member-forward", "member-deliver"}[c]
}

// WireGuard message sizes.
const (
	wgInitiationSize = 148
	wgResponseSize   = 92
	wgCookieSize     = 64
	wgMinDataSize    = 32
)

// IsWireGuard reports whether b is a WireGuard message.
func IsWireGuard(b []byte) bool {
	if len(b) < 4 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		return false
	}
	switch b[0] {
	case 1:
		return len(b) == wgInitiationSize
	case 2:
		return len(b) == wgResponseSize
	case 3:
		return len(b) == wgCookieSize
	case 4:
		return len(b) >= wgMinDataSize
	}
	return false
}

// Classify demultiplexes a datagram by its first bytes.
func Classify(b []byte) Class {
	if len(b) == 0 {
		return ClassUnknown
	}
	switch b[0] {
	case KindRelayControl:
		if len(b) >= 3 {
			return ClassRelayControl
		}
	case KindRelayData:
		if len(b) >= RelayDataOverhead {
			return ClassRelayData
		}
	case KindControl:
		if len(b) >= ControlOverhead {
			return ClassControl
		}
	case KindMemberForward:
		if len(b) >= MemberRelayOverhead {
			return ClassMemberForward
		}
	case KindMemberDeliver:
		if len(b) >= MemberRelayOverhead {
			return ClassMemberDeliver
		}
	case 1, 2, 3, 4:
		if IsWireGuard(b) {
			return ClassWireGuard
		}
	default:
		if stun.IsMessage(b) {
			return ClassSTUN
		}
	}
	return ClassUnknown
}

// Relay control message types.
const (
	RelayRegister    byte = 0x01
	RelayRegisterAck byte = 0x02
	RelayPing        byte = 0x04
	RelayPong        byte = 0x05
	RelayReject      byte = 0x06
	RelayUnregister  byte = 0x07
	RelayPeerJoined  byte = 0x08
	RelayEchoRequest byte = 0x09
	RelayEchoReply   byte = 0x0A
)

// Reject codes.
const (
	RejectBadRequest   byte = 1
	RejectSessionLimit byte = 2
	RejectSessionFull  byte = 3
	RejectRateLimited  byte = 4
	RejectBusy         byte = 5
	RejectVersion      byte = 6
)

// RelayControl is a decoded relay control message.
type RelayControl struct {
	Type        byte
	Session     SessionID
	Token       Token
	PeerPresent bool
	Addr        netip.AddrPort
	Nonce       uint64
	Code        byte
	DelaySec    byte
}

func relayHeader(b []byte, typ byte) []byte {
	return append(b, KindRelayControl, RelayVersion, typ)
}

// AppendRegister appends a Register message.
func AppendRegister(b []byte, s SessionID, t Token) []byte {
	b = relayHeader(b, RelayRegister)
	b = append(b, s[:]...)
	return append(b, t[:]...)
}

// AppendUnregister appends an Unregister message.
func AppendUnregister(b []byte, s SessionID, t Token) []byte {
	b = relayHeader(b, RelayUnregister)
	b = append(b, s[:]...)
	return append(b, t[:]...)
}

// AppendRegisterAck appends a RegisterAck message.
func AppendRegisterAck(b []byte, s SessionID, peerPresent bool, observed netip.AddrPort) []byte {
	b = relayHeader(b, RelayRegisterAck)
	b = append(b, s[:]...)
	flag := byte(0)
	if peerPresent {
		flag = 1
	}
	b = append(b, flag)
	return AppendAddr(b, observed)
}

// AppendPing appends a Ping message.
func AppendPing(b []byte, nonce uint64) []byte {
	b = relayHeader(b, RelayPing)
	return binary.BigEndian.AppendUint64(b, nonce)
}

// AppendPong appends a Pong message.
func AppendPong(b []byte, nonce uint64, observed netip.AddrPort) []byte {
	b = relayHeader(b, RelayPong)
	b = binary.BigEndian.AppendUint64(b, nonce)
	return AppendAddr(b, observed)
}

// AppendReject appends a Reject message.
func AppendReject(b []byte, s SessionID, code byte) []byte {
	b = relayHeader(b, RelayReject)
	b = append(b, s[:]...)
	return append(b, code)
}

// AppendPeerJoined appends a PeerJoined notification.
func AppendPeerJoined(b []byte, s SessionID) []byte {
	b = relayHeader(b, RelayPeerJoined)
	return append(b, s[:]...)
}

// AppendEchoRequest asks the relay to reply after delaySec seconds. It is
// used by natprobe to measure NAT mapping timeouts.
func AppendEchoRequest(b []byte, nonce uint64, delaySec byte) []byte {
	b = relayHeader(b, RelayEchoRequest)
	b = binary.BigEndian.AppendUint64(b, nonce)
	return append(b, delaySec)
}

// AppendEchoReply appends a delayed echo reply.
func AppendEchoReply(b []byte, nonce uint64, observed netip.AddrPort) []byte {
	b = relayHeader(b, RelayEchoReply)
	b = binary.BigEndian.AppendUint64(b, nonce)
	return AppendAddr(b, observed)
}

var errShort = errors.New("frame is truncated")

// ParseRelayControl decodes a relay control message.
func ParseRelayControl(b []byte) (RelayControl, error) {
	var m RelayControl
	if len(b) < 3 || b[0] != KindRelayControl {
		return m, errors.New("not a relay control frame")
	}
	if b[1] != RelayVersion {
		return m, errors.New("unsupported relay protocol version")
	}
	m.Type = b[2]
	p := b[3:]
	switch m.Type {
	case RelayRegister, RelayUnregister:
		if len(p) < 24 {
			return m, errShort
		}
		copy(m.Session[:], p[:16])
		copy(m.Token[:], p[16:24])
	case RelayRegisterAck:
		if len(p) < 17 {
			return m, errShort
		}
		copy(m.Session[:], p[:16])
		m.PeerPresent = p[16] == 1
		a, _, err := ParseAddr(p[17:])
		if err != nil {
			return m, err
		}
		m.Addr = a
	case RelayPing:
		if len(p) < 8 {
			return m, errShort
		}
		m.Nonce = binary.BigEndian.Uint64(p)
	case RelayPong, RelayEchoReply:
		if len(p) < 8 {
			return m, errShort
		}
		m.Nonce = binary.BigEndian.Uint64(p)
		a, _, err := ParseAddr(p[8:])
		if err != nil {
			return m, err
		}
		m.Addr = a
	case RelayReject:
		if len(p) < 17 {
			return m, errShort
		}
		copy(m.Session[:], p[:16])
		m.Code = p[16]
	case RelayPeerJoined:
		if len(p) < 16 {
			return m, errShort
		}
		copy(m.Session[:], p[:16])
	case RelayEchoRequest:
		if len(p) < 9 {
			return m, errShort
		}
		m.Nonce = binary.BigEndian.Uint64(p)
		m.DelaySec = p[8]
	default:
		return m, errors.New("unknown relay control type")
	}
	return m, nil
}

// AppendAddr encodes an address as family (4 or 6), port and IP bytes.
func AppendAddr(b []byte, a netip.AddrPort) []byte {
	ip := a.Addr().Unmap()
	if ip.Is4() {
		b = append(b, 4)
		b = binary.BigEndian.AppendUint16(b, a.Port())
		v := ip.As4()
		return append(b, v[:]...)
	}
	if ip.Is6() {
		b = append(b, 6)
		b = binary.BigEndian.AppendUint16(b, a.Port())
		v := ip.As16()
		return append(b, v[:]...)
	}
	return append(b, 0)
}

// ParseAddr decodes an address written by AppendAddr and returns the number
// of bytes consumed.
func ParseAddr(b []byte) (netip.AddrPort, int, error) {
	if len(b) < 1 {
		return netip.AddrPort{}, 0, errShort
	}
	switch b[0] {
	case 0:
		return netip.AddrPort{}, 1, nil
	case 4:
		if len(b) < 7 {
			return netip.AddrPort{}, 0, errShort
		}
		var v [4]byte
		copy(v[:], b[3:7])
		return netip.AddrPortFrom(netip.AddrFrom4(v), binary.BigEndian.Uint16(b[1:3])), 7, nil
	case 6:
		if len(b) < 19 {
			return netip.AddrPort{}, 0, errShort
		}
		var v [16]byte
		copy(v[:], b[3:19])
		return netip.AddrPortFrom(netip.AddrFrom16(v), binary.BigEndian.Uint16(b[1:3])), 19, nil
	}
	return netip.AddrPort{}, 0, errors.New("unknown address family")
}

// AppendRelayData wraps payload for the shared relay.
func AppendRelayData(b []byte, s SessionID, payload []byte) []byte {
	b = append(b, KindRelayData)
	b = append(b, s[:]...)
	return append(b, payload...)
}

// ParseRelayData splits a relay data frame.
func ParseRelayData(b []byte) (SessionID, []byte, bool) {
	var s SessionID
	if len(b) < RelayDataOverhead || b[0] != KindRelayData {
		return s, nil, false
	}
	copy(s[:], b[1:17])
	return s, b[17:], true
}

// AppendMemberForward wraps payload for a relaying member. s is the session
// between the sender and the relaying member; target is the final peer.
func AppendMemberForward(b []byte, s SessionID, target [8]byte, payload []byte) []byte {
	b = append(b, KindMemberForward)
	b = append(b, s[:]...)
	b = append(b, target[:]...)
	return append(b, payload...)
}

// AppendMemberDeliver wraps payload delivered by a relaying member. s is the
// session between the relaying member and the receiver; source is the
// original sender.
func AppendMemberDeliver(b []byte, s SessionID, source [8]byte, payload []byte) []byte {
	b = append(b, KindMemberDeliver)
	b = append(b, s[:]...)
	b = append(b, source[:]...)
	return append(b, payload...)
}

// ParseMemberFrame splits a member relay frame of either direction.
func ParseMemberFrame(b []byte) (SessionID, [8]byte, []byte, bool) {
	var s SessionID
	var id [8]byte
	if len(b) < MemberRelayOverhead || (b[0] != KindMemberForward && b[0] != KindMemberDeliver) {
		return s, id, nil, false
	}
	copy(s[:], b[1:17])
	copy(id[:], b[17:25])
	return s, id, b[25:], true
}

// MaxStreamFrame is the largest frame carried over the TCP fallback.
const MaxStreamFrame = 2048

// AppendStreamFrame prefixes frame with its 16-bit big-endian length for the
// TCP fallback transport.
func AppendStreamFrame(b, frame []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(frame)))
	return append(b, frame...)
}

// ReadStreamFrame reads one length-prefixed frame into buf and returns it.
func ReadStreamFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > MaxStreamFrame || n > len(buf) {
		return nil, errors.New("invalid stream frame length")
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// SealControl encrypts a control message with XChaCha20-Poly1305. The kind
// byte and session ID are authenticated as associated data.
func SealControl(b []byte, key *[32]byte, s SessionID, plain []byte) []byte {
	aead, _ := chacha20poly1305.NewX(key[:])
	start := len(b)
	b = append(b, KindControl)
	b = append(b, s[:]...)
	var nonce [chacha20poly1305.NonceSizeX]byte
	rand.Read(nonce[:])
	b = append(b, nonce[:]...)
	return aead.Seal(b, nonce[:], plain, b[start:start+17])
}

// ControlSession returns the session ID of a control frame.
func ControlSession(b []byte) (SessionID, bool) {
	var s SessionID
	if len(b) < ControlOverhead || b[0] != KindControl {
		return s, false
	}
	copy(s[:], b[1:17])
	return s, true
}

// OpenControl authenticates and decrypts a control frame.
func OpenControl(key *[32]byte, b []byte) ([]byte, error) {
	if len(b) < ControlOverhead || b[0] != KindControl {
		return nil, errors.New("not a control frame")
	}
	aead, _ := chacha20poly1305.NewX(key[:])
	nonce := b[17 : 17+chacha20poly1305.NonceSizeX]
	return aead.Open(nil, nonce, b[17+chacha20poly1305.NonceSizeX:], b[:17])
}
