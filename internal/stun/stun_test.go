package stun

import (
	"encoding/hex"
	"net/netip"
	"testing"
)

func TestBindingRoundTripIPv4(t *testing.T) {
	tx := NewTxID()
	req := BindingRequest(tx)
	if !IsMessage(req) {
		t.Fatal("request not recognised as STUN")
	}
	gotTx, ok := ParseBindingRequest(req)
	if !ok || gotTx != tx {
		t.Fatal("request parse failed")
	}
	addr := netip.MustParseAddrPort("203.0.113.7:54321")
	resp := BindingResponse(tx, addr)
	rtx, raddr, err := ParseBindingResponse(resp)
	if err != nil || rtx != tx || raddr != addr {
		t.Fatalf("response parse: %v %v %v", rtx, raddr, err)
	}
}

func TestBindingRoundTripIPv6(t *testing.T) {
	tx := NewTxID()
	addr := netip.MustParseAddrPort("[2001:db8::1234]:3478")
	_, raddr, err := ParseBindingResponse(BindingResponse(tx, addr))
	if err != nil || raddr != addr {
		t.Fatalf("got %v %v", raddr, err)
	}
}

// RFC 5769 section 2.2 sample IPv4 response (without FINGERPRINT check).
func TestRFC5769Vector(t *testing.T) {
	raw, _ := hex.DecodeString("0101003c2112a442b7e7a701bc34d686fa87dfae" +
		"8022000b7465737420766563746f7220" +
		"002000080001a147e112a643" +
		"000800149e90e3d8c39f6e3cf4c95e83e1fcbcb2cb6a2cae" +
		"80280004c07d4c96")
	_, addr, err := ParseBindingResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddrPort("192.0.2.1:32853") {
		t.Fatalf("got %v", addr)
	}
}

func TestRejectsGarbage(t *testing.T) {
	if IsMessage([]byte{1, 0, 0, 0}) {
		t.Fatal("short packet accepted")
	}
	b := BindingRequest(NewTxID())
	b[4] ^= 0xff
	if IsMessage(b) {
		t.Fatal("bad cookie accepted")
	}
	if _, _, err := ParseBindingResponse(BindingRequest(NewTxID())); err == nil {
		t.Fatal("request parsed as response")
	}
}
