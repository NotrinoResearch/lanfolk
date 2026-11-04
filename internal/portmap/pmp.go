package portmap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"time"
)

// Errors reported by the PCP and NAT-PMP clients.
var (
	errNoResponse         = errors.New("no response from gateway")
	errIgnore             = errors.New("unrelated packet") // internal: keep waiting
	errUnsupportedVersion = errors.New("gateway does not support this protocol version")
)

// resultError is a non-success result code returned by the gateway.
type resultError struct {
	proto Protocol
	code  uint16
	text  string
}

func (e *resultError) Error() string {
	return fmt.Sprintf("%s: gateway refused request: %s (%d)", e.proto, e.text, e.code)
}

func (e *resultError) Is(target error) bool {
	return target == errUnsupportedVersion && e.code == 1
}

var natpmpResults = map[uint16]string{
	1: "UNSUPP_VERSION",
	2: "NOT_AUTHORIZED",
	3: "NETWORK_FAILURE",
	4: "OUT_OF_RESOURCES",
	5: "UNSUPP_OPCODE",
}

var pcpResults = map[uint16]string{
	1:  "UNSUPP_VERSION",
	2:  "NOT_AUTHORIZED",
	3:  "MALFORMED_REQUEST",
	4:  "UNSUPP_OPCODE",
	5:  "UNSUPP_OPTION",
	6:  "MALFORMED_OPTION",
	7:  "NETWORK_FAILURE",
	8:  "NO_RESOURCES",
	9:  "UNSUPP_PROTOCOL",
	10: "USER_EX_QUOTA",
	11: "CANNOT_PROVIDE_EXTERNAL",
	12: "ADDRESS_MISMATCH",
	13: "EXCESSIVE_REMOTE_PEERS",
}

func newResultError(p Protocol, code uint16) error {
	t := natpmpResults[code]
	if p == ProtoPCP {
		t = pcpResults[code]
	}
	if t == "" {
		t = "unknown result"
	}
	return &resultError{proto: p, code: code, text: t}
}

// exchange sends req from a fresh socket bound to local and retransmits it
// with exponential backoff (starting at rto, doubling) until match accepts a
// reply, the gateway refuses, or ctx ends. match returns (false, nil) for
// packets that are not the awaited reply.
func exchange(ctx context.Context, local, gw netip.Addr, port int, rto time.Duration, req []byte,
	match func(resp []byte) (done bool, err error)) error {

	conn, err := net.DialUDP("udp4",
		&net.UDPAddr{IP: local.AsSlice()},
		&net.UDPAddr{IP: gw.AsSlice(), Port: port})
	if err != nil {
		return err
	}
	defer conn.Close()
	// Interrupt a pending read as soon as ctx ends.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	buf := make([]byte, 1100)
	for {
		if _, err := conn.Write(req); err != nil {
			return ctxOr(ctx, err)
		}
		dl := time.Now().Add(rto)
		if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
			dl = cd
		}
		conn.SetReadDeadline(dl)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					break
				}
				return ctxOr(ctx, err)
			}
			if done, err := match(buf[:n]); done {
				return err
			}
		}
		if ctx.Err() != nil {
			return errNoResponse
		}
		rto *= 2
	}
}

func ctxOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errNoResponse
	}
	return err
}

// matcher adapts a decode function that reports errIgnore for unrelated
// packets to the exchange callback.
func matcher(decode func([]byte) error) func([]byte) (bool, error) {
	return func(p []byte) (bool, error) {
		err := decode(p)
		if errors.Is(err, errIgnore) {
			return false, nil
		}
		return true, err
	}
}

// --- NAT-PMP (RFC 6886) ---

const (
	natpmpOpExternal  = 0
	natpmpOpMapUDP    = 1
	natpmpReplyFlag   = 128
	natpmpMapReqLen   = 12
	natpmpMapRespLen  = 16
	natpmpExtRespLen  = 12
	natpmpMapUDPReply = natpmpReplyFlag | natpmpOpMapUDP
	natpmpExtReply    = natpmpReplyFlag | natpmpOpExternal
)

func encodeNATPMPExternalRequest() []byte { return []byte{0, natpmpOpExternal} }

// decodeNATPMPExternal parses an external-address response.
func decodeNATPMPExternal(p []byte) (netip.Addr, error) {
	if len(p) < 4 || p[0] != 0 || p[1] != natpmpExtReply {
		return netip.Addr{}, errIgnore
	}
	if code := binary.BigEndian.Uint16(p[2:4]); code != 0 {
		return netip.Addr{}, newResultError(ProtoNATPMP, code)
	}
	if len(p) < natpmpExtRespLen {
		return netip.Addr{}, errIgnore
	}
	return netip.AddrFrom4([4]byte(p[8:12])), nil
}

func encodeNATPMPMapRequest(intPort, extPort uint16, lifetimeSec uint32) []byte {
	b := make([]byte, natpmpMapReqLen)
	b[1] = natpmpOpMapUDP
	binary.BigEndian.PutUint16(b[4:], intPort)
	binary.BigEndian.PutUint16(b[6:], extPort)
	binary.BigEndian.PutUint32(b[8:], lifetimeSec)
	return b
}

type natpmpMapResp struct {
	extPort     uint16
	lifetimeSec uint32
}

func decodeNATPMPMap(p []byte, intPort uint16) (natpmpMapResp, error) {
	if len(p) < 4 || p[0] != 0 || p[1] != natpmpMapUDPReply {
		return natpmpMapResp{}, errIgnore
	}
	if code := binary.BigEndian.Uint16(p[2:4]); code != 0 {
		return natpmpMapResp{}, newResultError(ProtoNATPMP, code)
	}
	if len(p) < natpmpMapRespLen || binary.BigEndian.Uint16(p[8:]) != intPort {
		return natpmpMapResp{}, errIgnore
	}
	return natpmpMapResp{
		extPort:     binary.BigEndian.Uint16(p[10:]),
		lifetimeSec: binary.BigEndian.Uint32(p[12:]),
	}, nil
}

type natpmp struct {
	m         *Mapper
	gw, local netip.Addr
	suggested uint16
}

func newNATPMP(m *Mapper, gw, local netip.Addr, suggested uint16) *natpmp {
	return &natpmp{m: m, gw: gw, local: local, suggested: suggested}
}

func (b *natpmp) proto() Protocol { return ProtoNATPMP }

func (b *natpmp) acquire(ctx context.Context) (acquired, error) {
	var ext netip.Addr
	err := exchange(ctx, b.local, b.gw, b.m.opts.PMPPort, b.m.initialRTO, encodeNATPMPExternalRequest(),
		matcher(func(p []byte) (err error) {
			ext, err = decodeNATPMPExternal(p)
			return err
		}))
	if err != nil {
		return acquired{}, err
	}
	lifetime := lifetimeSeconds(b.m.opts.Lifetime)
	var mr natpmpMapResp
	err = exchange(ctx, b.local, b.gw, b.m.opts.PMPPort, b.m.initialRTO,
		encodeNATPMPMapRequest(b.m.localPort, b.suggested, lifetime),
		matcher(func(p []byte) (err error) {
			mr, err = decodeNATPMPMap(p, b.m.localPort)
			return err
		}))
	if err != nil {
		return acquired{}, err
	}
	if mr.lifetimeSec == 0 || mr.extPort == 0 {
		return acquired{}, errors.New("nat-pmp: gateway granted no mapping")
	}
	b.suggested = mr.extPort
	return acquired{
		ext:      netip.AddrPortFrom(ext, mr.extPort),
		lifetime: time.Duration(mr.lifetimeSec) * time.Second,
	}, nil
}

func (b *natpmp) release(ctx context.Context) error {
	return exchange(ctx, b.local, b.gw, b.m.opts.PMPPort, b.m.initialRTO,
		encodeNATPMPMapRequest(b.m.localPort, 0, 0),
		matcher(func(p []byte) error {
			_, err := decodeNATPMPMap(p, b.m.localPort)
			return err
		}))
}

// --- PCP (RFC 6887) ---

const (
	pcpVersion    = 2
	pcpOpMap      = 1
	pcpReplyFlag  = 0x80
	pcpHeaderLen  = 24
	pcpMapDataLen = 36
	pcpMapLen     = pcpHeaderLen + pcpMapDataLen
	protoUDP      = 17
)

func encodePCPMapRequest(client netip.Addr, nonce [12]byte, intPort, extPort uint16, lifetimeSec uint32) []byte {
	b := make([]byte, pcpMapLen)
	b[0] = pcpVersion
	b[1] = pcpOpMap
	binary.BigEndian.PutUint32(b[4:], lifetimeSec)
	c := client.As16() // IPv4 becomes ::ffff:a.b.c.d
	copy(b[8:24], c[:])
	copy(b[24:36], nonce[:])
	b[36] = protoUDP
	binary.BigEndian.PutUint16(b[40:], intPort)
	binary.BigEndian.PutUint16(b[42:], extPort)
	// Suggested external IP (b[44:60]) stays all zeros: any address.
	return b
}

type pcpMapResp struct {
	ext         netip.AddrPort
	lifetimeSec uint32
}

func decodePCPMap(p []byte, nonce [12]byte, intPort uint16) (pcpMapResp, error) {
	if len(p) < 4 {
		return pcpMapResp{}, errIgnore
	}
	if p[0] != pcpVersion {
		// A NAT-PMP-only gateway answers an unknown version with
		// version 0 and result code 1.
		if p[1]&pcpReplyFlag != 0 && binary.BigEndian.Uint16(p[2:4]) == 1 {
			return pcpMapResp{}, newResultError(ProtoPCP, 1)
		}
		return pcpMapResp{}, errIgnore
	}
	if len(p) < pcpHeaderLen || p[1] != pcpReplyFlag|pcpOpMap {
		return pcpMapResp{}, errIgnore
	}
	if code := p[3]; code != 0 {
		return pcpMapResp{}, newResultError(ProtoPCP, uint16(code))
	}
	if len(p) < pcpMapLen {
		return pcpMapResp{}, errIgnore
	}
	if [12]byte(p[24:36]) != nonce || p[36] != protoUDP || binary.BigEndian.Uint16(p[40:]) != intPort {
		return pcpMapResp{}, errIgnore
	}
	ext := netip.AddrFrom16([16]byte(p[44:60])).Unmap()
	return pcpMapResp{
		ext:         netip.AddrPortFrom(ext, binary.BigEndian.Uint16(p[42:])),
		lifetimeSec: binary.BigEndian.Uint32(p[4:]),
	}, nil
}

type pcp struct {
	m         *Mapper
	gw, local netip.Addr
	suggested uint16
}

func newPCP(m *Mapper, gw, local netip.Addr, suggested uint16) *pcp {
	return &pcp{m: m, gw: gw, local: local, suggested: suggested}
}

func (b *pcp) proto() Protocol { return ProtoPCP }

func (b *pcp) acquire(ctx context.Context) (acquired, error) {
	req := encodePCPMapRequest(b.local, b.m.nonce, b.m.localPort, b.suggested, lifetimeSeconds(b.m.opts.Lifetime))
	var r pcpMapResp
	err := exchange(ctx, b.local, b.gw, b.m.opts.PMPPort, b.m.initialRTO, req,
		matcher(func(p []byte) (err error) {
			r, err = decodePCPMap(p, b.m.nonce, b.m.localPort)
			return err
		}))
	if err != nil {
		return acquired{}, err
	}
	if r.lifetimeSec == 0 {
		return acquired{}, errors.New("pcp: gateway granted no mapping")
	}
	b.suggested = r.ext.Port()
	return acquired{ext: r.ext, lifetime: time.Duration(r.lifetimeSec) * time.Second}, nil
}

func (b *pcp) release(ctx context.Context) error {
	req := encodePCPMapRequest(b.local, b.m.nonce, b.m.localPort, 0, 0)
	return exchange(ctx, b.local, b.gw, b.m.opts.PMPPort, b.m.initialRTO, req,
		matcher(func(p []byte) error {
			_, err := decodePCPMap(p, b.m.nonce, b.m.localPort)
			return err
		}))
}

// lifetimeSeconds converts d to whole seconds, at least one.
func lifetimeSeconds(d time.Duration) uint32 {
	s := int64((d + time.Second - 1) / time.Second)
	return uint32(min(max(s, 1), 1<<32-1))
}
