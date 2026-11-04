// Package peermsg encodes the peer-to-peer control messages that travel in
// encrypted control frames (wire.SealControl): Hello, MembershipUpdate,
// Candidates, PathProbe, PathProbeAck, RelayOffer and Bye.
//
// Plaintext layout: boot (u64) | counter (u64) | type (u8) | body. Boot and
// counter give replay protection; see docs/PROTOCOL.md.
package peermsg

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Message types.
const (
	TypeHello            uint8 = 1
	TypeMembershipUpdate uint8 = 2
	TypeCandidates       uint8 = 3
	TypePathProbe        uint8 = 4
	TypePathProbeAck     uint8 = 5
	TypeRelayOffer       uint8 = 6
	TypeBye              uint8 = 7
)

// Limits keep every message inside one datagram on every path.
const (
	MaxPlaintext    = 1200
	HeaderLen       = 17
	MaxCandidates   = 16
	MaxOfferEntries = 100
	MaxNetRefs      = 32
	FragmentSize    = 1100
)

// Hello flags and capabilities.
const (
	FlagRelayOnly uint8 = 1 << 0

	CapMemberRelay uint32 = 1 << 0 // willing to relay for other members
	CapLanFrame    uint32 = 1 << 1 // understands broadcast/multicast LanFrames
)

// Header precedes every message.
type Header struct {
	Boot    uint64
	Counter uint64
	Type    uint8
}

// Message is one control message.
type Message interface {
	Type() uint8
	appendBody([]byte) []byte
	parseBody([]byte) error
}

// NetRef names a network and the member list version the sender has.
type NetRef struct {
	ID      network.ID
	Version uint64
}

// Hello announces presence, protocol version and known member lists.
type Hello struct {
	ProtocolVersion uint8
	Flags           uint8
	Nickname        string
	Capabilities    uint32
	ActiveNetwork   network.ID
	Networks        []NetRef
}

// MembershipUpdate carries one fragment of a signed member list.
type MembershipUpdate struct {
	MsgID uint32
	Index uint8
	Total uint8
	Data  []byte
}

// Candidate is one address the sender can be reached at.
type Candidate struct {
	Source uint8
	Addr   netip.AddrPort
}

// Candidates lists the sender's current candidate addresses.
type Candidates struct {
	Gen   uint32
	Addrs []Candidate
}

// PathProbe tests a path and measures RTT.
type PathProbe struct {
	ProbeID uint64
	PathID  uint32
}

// PathProbeAck answers a probe on the same path. Observed is the source
// address the probe arrived from (zero for relayed paths).
type PathProbeAck struct {
	ProbeID  uint64
	PathID   uint32
	Observed netip.AddrPort
}

// OfferEntry advertises a direct path from the relaying member to Peer.
type OfferEntry struct {
	Peer identity.KeyID
	RTT  time.Duration
}

// RelayOffer lists peers the sender can relay to.
type RelayOffer struct {
	Entries []OfferEntry
}

// Bye announces that the sender is going offline.
type Bye struct{}

func (*Hello) Type() uint8            { return TypeHello }
func (*MembershipUpdate) Type() uint8 { return TypeMembershipUpdate }
func (*Candidates) Type() uint8       { return TypeCandidates }
func (*PathProbe) Type() uint8        { return TypePathProbe }
func (*PathProbeAck) Type() uint8     { return TypePathProbeAck }
func (*RelayOffer) Type() uint8       { return TypeRelayOffer }
func (*Bye) Type() uint8              { return TypeBye }

// Marshal encodes a message with its header. Lists longer than the limits
// are truncated.
func Marshal(h Header, m Message) []byte {
	b := make([]byte, 0, 128)
	b = binary.BigEndian.AppendUint64(b, h.Boot)
	b = binary.BigEndian.AppendUint64(b, h.Counter)
	b = append(b, m.Type())
	return m.appendBody(b)
}

var errShort = errors.New("control message is truncated")

// Unmarshal decodes a message.
func Unmarshal(b []byte) (Header, Message, error) {
	var h Header
	if len(b) < HeaderLen {
		return h, nil, errShort
	}
	h.Boot = binary.BigEndian.Uint64(b[0:8])
	h.Counter = binary.BigEndian.Uint64(b[8:16])
	h.Type = b[16]
	var m Message
	switch h.Type {
	case TypeHello:
		m = &Hello{}
	case TypeMembershipUpdate:
		m = &MembershipUpdate{}
	case TypeCandidates:
		m = &Candidates{}
	case TypePathProbe:
		m = &PathProbe{}
	case TypePathProbeAck:
		m = &PathProbeAck{}
	case TypeRelayOffer:
		m = &RelayOffer{}
	case TypeBye:
		m = &Bye{}
	default:
		return h, nil, errors.New("unknown control message type")
	}
	if err := m.parseBody(b[HeaderLen:]); err != nil {
		return h, nil, err
	}
	return h, m, nil
}

func appendStr(b []byte, s string, max int) []byte {
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	b = append(b, byte(len(s)))
	return append(b, s...)
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errShort
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u16() uint16 { return binary.BigEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32 { return binary.BigEndian.Uint32(r.take(4)) }
func (r *reader) u64() uint64 { return binary.BigEndian.Uint64(r.take(8)) }

func (r *reader) str(max int) string {
	n := int(r.u8())
	if n > max {
		r.err = errors.New("text field too long")
		return ""
	}
	s := r.take(n)
	if !utf8.Valid(s) {
		r.err = errors.New("text field is not UTF-8")
		return ""
	}
	return string(s)
}

func (r *reader) addr() netip.AddrPort {
	if r.err != nil {
		return netip.AddrPort{}
	}
	a, n, err := wire.ParseAddr(r.b)
	if err != nil {
		r.err = err
		return netip.AddrPort{}
	}
	r.b = r.b[n:]
	return a
}

func (m *Hello) appendBody(b []byte) []byte {
	b = append(b, m.ProtocolVersion, m.Flags)
	b = appendStr(b, m.Nickname, network.MaxNicknameLen)
	b = binary.BigEndian.AppendUint32(b, m.Capabilities)
	b = append(b, m.ActiveNetwork[:]...)
	refs := m.Networks
	if len(refs) > MaxNetRefs {
		refs = refs[:MaxNetRefs]
	}
	b = append(b, byte(len(refs)))
	for _, n := range refs {
		b = append(b, n.ID[:]...)
		b = binary.BigEndian.AppendUint64(b, n.Version)
	}
	return b
}

func (m *Hello) parseBody(p []byte) error {
	r := &reader{b: p}
	m.ProtocolVersion = r.u8()
	m.Flags = r.u8()
	m.Nickname = r.str(network.MaxNicknameLen)
	m.Capabilities = r.u32()
	copy(m.ActiveNetwork[:], r.take(16))
	n := int(r.u8())
	if n > MaxNetRefs {
		return errors.New("too many network references")
	}
	m.Networks = nil
	for i := 0; i < n && r.err == nil; i++ {
		var ref NetRef
		copy(ref.ID[:], r.take(16))
		ref.Version = r.u64()
		m.Networks = append(m.Networks, ref)
	}
	return r.err
}

func (m *MembershipUpdate) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, m.MsgID)
	b = append(b, m.Index, m.Total)
	b = binary.BigEndian.AppendUint16(b, uint16(len(m.Data)))
	return append(b, m.Data...)
}

func (m *MembershipUpdate) parseBody(p []byte) error {
	r := &reader{b: p}
	m.MsgID = r.u32()
	m.Index = r.u8()
	m.Total = r.u8()
	n := int(r.u16())
	if n > FragmentSize {
		return errors.New("fragment too large")
	}
	m.Data = append([]byte(nil), r.take(n)...)
	return r.err
}

func (m *Candidates) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, m.Gen)
	addrs := m.Addrs
	if len(addrs) > MaxCandidates {
		addrs = addrs[:MaxCandidates]
	}
	b = append(b, byte(len(addrs)))
	for _, a := range addrs {
		b = append(b, a.Source)
		b = wire.AppendAddr(b, a.Addr)
	}
	return b
}

func (m *Candidates) parseBody(p []byte) error {
	r := &reader{b: p}
	m.Gen = r.u32()
	n := int(r.u8())
	if n > MaxCandidates {
		return errors.New("too many candidates")
	}
	m.Addrs = nil
	for i := 0; i < n && r.err == nil; i++ {
		src := r.u8()
		a := r.addr()
		if r.err == nil {
			m.Addrs = append(m.Addrs, Candidate{Source: src, Addr: a})
		}
	}
	return r.err
}

func (m *PathProbe) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint64(b, m.ProbeID)
	return binary.BigEndian.AppendUint32(b, m.PathID)
}

func (m *PathProbe) parseBody(p []byte) error {
	r := &reader{b: p}
	m.ProbeID = r.u64()
	m.PathID = r.u32()
	return r.err
}

func (m *PathProbeAck) appendBody(b []byte) []byte {
	b = binary.BigEndian.AppendUint64(b, m.ProbeID)
	b = binary.BigEndian.AppendUint32(b, m.PathID)
	return wire.AppendAddr(b, m.Observed)
}

func (m *PathProbeAck) parseBody(p []byte) error {
	r := &reader{b: p}
	m.ProbeID = r.u64()
	m.PathID = r.u32()
	m.Observed = r.addr()
	return r.err
}

func (m *RelayOffer) appendBody(b []byte) []byte {
	e := m.Entries
	if len(e) > MaxOfferEntries {
		e = e[:MaxOfferEntries]
	}
	b = append(b, byte(len(e)))
	for _, x := range e {
		b = append(b, x.Peer[:]...)
		us := x.RTT.Microseconds()
		if us < 0 {
			us = 0
		}
		if us > 0xFFFFFFFF {
			us = 0xFFFFFFFF
		}
		b = binary.BigEndian.AppendUint32(b, uint32(us))
	}
	return b
}

func (m *RelayOffer) parseBody(p []byte) error {
	r := &reader{b: p}
	n := int(r.u8())
	if n > MaxOfferEntries {
		return errors.New("too many offer entries")
	}
	m.Entries = nil
	for i := 0; i < n && r.err == nil; i++ {
		var e OfferEntry
		copy(e.Peer[:], r.take(8))
		e.RTT = time.Duration(r.u32()) * time.Microsecond
		m.Entries = append(m.Entries, e)
	}
	return r.err
}

func (*Bye) appendBody(b []byte) []byte { return b }
func (*Bye) parseBody([]byte) error     { return nil }

// Fragment splits a signed member list into MembershipUpdate messages.
func Fragment(data []byte, msgID uint32) []*MembershipUpdate {
	total := (len(data) + FragmentSize - 1) / FragmentSize
	if total == 0 {
		total = 1
	}
	out := make([]*MembershipUpdate, 0, total)
	for i := 0; i < total; i++ {
		end := (i + 1) * FragmentSize
		if end > len(data) {
			end = len(data)
		}
		out = append(out, &MembershipUpdate{MsgID: msgID, Index: uint8(i), Total: uint8(total),
			Data: data[i*FragmentSize : end]})
	}
	return out
}

// Reassembler collects MembershipUpdate fragments from one peer.
type Reassembler struct {
	timeout time.Duration
	msgs    map[uint32]*partial
}

type partial struct {
	started time.Time
	parts   [][]byte
	have    int
	size    int
}

// MaxReassembled bounds the memory one peer can make us hold.
const MaxReassembled = 64 * 1024

// NewReassembler creates a Reassembler that drops incomplete messages after
// timeout.
func NewReassembler(timeout time.Duration) *Reassembler {
	return &Reassembler{timeout: timeout, msgs: make(map[uint32]*partial)}
}

// Pending returns the number of incomplete messages.
func (r *Reassembler) Pending() int { return len(r.msgs) }

// Add stores a fragment and returns the complete payload when the last
// fragment arrives.
func (r *Reassembler) Add(f *MembershipUpdate, now time.Time) []byte {
	for id, p := range r.msgs {
		if now.Sub(p.started) > r.timeout {
			delete(r.msgs, id)
		}
	}
	if f.Total == 0 || f.Index >= f.Total || int(f.Total)*FragmentSize > MaxReassembled {
		return nil
	}
	p := r.msgs[f.MsgID]
	if p == nil {
		if len(r.msgs) >= 4 {
			return nil
		}
		p = &partial{started: now, parts: make([][]byte, f.Total)}
		r.msgs[f.MsgID] = p
	}
	if len(p.parts) != int(f.Total) {
		delete(r.msgs, f.MsgID)
		return nil
	}
	if p.parts[f.Index] == nil {
		p.parts[f.Index] = append([]byte(nil), f.Data...)
		p.have++
		p.size += len(f.Data)
	}
	if p.have < len(p.parts) {
		return nil
	}
	delete(r.msgs, f.MsgID)
	out := make([]byte, 0, p.size)
	for _, part := range p.parts {
		out = append(out, part...)
	}
	return out
}
