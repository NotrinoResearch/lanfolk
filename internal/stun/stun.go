// Package stun implements the subset of STUN (RFC 5389) that Lanfolk needs:
// Binding requests and responses with XOR-MAPPED-ADDRESS.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"
)

const (
	magicCookie     = 0x2112A442
	headerLen       = 20
	typeBindingReq  = 0x0001
	typeBindingResp = 0x0101

	attrMappedAddress    = 0x0001
	attrXorMappedAddress = 0x0020
	attrSoftware         = 0x8022
)

// TxID is a STUN transaction ID.
type TxID [12]byte

// NewTxID returns a random transaction ID.
func NewTxID() TxID {
	var t TxID
	rand.Read(t[:])
	return t
}

// IsMessage reports whether b looks like a STUN message.
func IsMessage(b []byte) bool {
	return len(b) >= headerLen && b[0]&0xC0 == 0 &&
		binary.BigEndian.Uint32(b[4:8]) == magicCookie &&
		int(binary.BigEndian.Uint16(b[2:4]))+headerLen <= len(b)
}

func header(typ uint16, length int, tx TxID) []byte {
	b := make([]byte, headerLen, headerLen+length)
	binary.BigEndian.PutUint16(b[0:2], typ)
	binary.BigEndian.PutUint16(b[2:4], uint16(length))
	binary.BigEndian.PutUint32(b[4:8], magicCookie)
	copy(b[8:20], tx[:])
	return b
}

// BindingRequest builds a Binding request without attributes.
func BindingRequest(tx TxID) []byte { return header(typeBindingReq, 0, tx) }

// ParseBindingRequest returns the transaction ID of a Binding request.
func ParseBindingRequest(b []byte) (TxID, bool) {
	var tx TxID
	if !IsMessage(b) || binary.BigEndian.Uint16(b[0:2]) != typeBindingReq {
		return tx, false
	}
	copy(tx[:], b[8:20])
	return tx, true
}

// BindingResponse builds a success response carrying addr as
// XOR-MAPPED-ADDRESS.
func BindingResponse(tx TxID, addr netip.AddrPort) []byte {
	ip := addr.Addr().Unmap()
	alen := 8
	if ip.Is6() {
		alen = 20
	}
	b := header(typeBindingResp, 4+alen, tx)
	attr := make([]byte, 4+alen)
	binary.BigEndian.PutUint16(attr[0:2], attrXorMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(alen))
	binary.BigEndian.PutUint16(attr[6:8], addr.Port()^uint16(magicCookie>>16))
	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], tx[:])
	if ip.Is4() {
		attr[5] = 0x01
		a := ip.As4()
		for i := 0; i < 4; i++ {
			attr[8+i] = a[i] ^ key[i]
		}
	} else {
		attr[5] = 0x02
		a := ip.As16()
		for i := 0; i < 16; i++ {
			attr[8+i] = a[i] ^ key[i]
		}
	}
	return append(b, attr...)
}

// ParseBindingResponse extracts the transaction ID and mapped address of a
// Binding success response.
func ParseBindingResponse(b []byte) (TxID, netip.AddrPort, error) {
	var tx TxID
	if !IsMessage(b) || binary.BigEndian.Uint16(b[0:2]) != typeBindingResp {
		return tx, netip.AddrPort{}, errors.New("not a STUN binding response")
	}
	copy(tx[:], b[8:20])
	var key [16]byte
	binary.BigEndian.PutUint32(key[0:4], magicCookie)
	copy(key[4:], tx[:])
	end := headerLen + int(binary.BigEndian.Uint16(b[2:4]))
	var mapped netip.AddrPort
	for off := headerLen; off+4 <= end; {
		typ := binary.BigEndian.Uint16(b[off : off+2])
		l := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		v := off + 4
		if v+l > end {
			break
		}
		val := b[v : v+l]
		switch typ {
		case attrXorMappedAddress:
			if a, ok := decodeAddr(val, &key); ok {
				return tx, a, nil
			}
		case attrMappedAddress:
			if a, ok := decodeAddr(val, nil); ok {
				mapped = a
			}
		}
		off = v + (l+3)&^3
	}
	if mapped.IsValid() {
		return tx, mapped, nil
	}
	return tx, netip.AddrPort{}, errors.New("STUN response has no mapped address")
}

func decodeAddr(v []byte, key *[16]byte) (netip.AddrPort, bool) {
	if len(v) < 8 {
		return netip.AddrPort{}, false
	}
	port := binary.BigEndian.Uint16(v[2:4])
	if key != nil {
		port ^= uint16(magicCookie >> 16)
	}
	switch v[1] {
	case 0x01:
		var a [4]byte
		copy(a[:], v[4:8])
		if key != nil {
			for i := range a {
				a[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom4(a), port), true
	case 0x02:
		if len(v) < 20 {
			return netip.AddrPort{}, false
		}
		var a [16]byte
		copy(a[:], v[4:20])
		if key != nil {
			for i := range a {
				a[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(a), port), true
	}
	return netip.AddrPort{}, false
}
