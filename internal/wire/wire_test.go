package wire

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/NotrinoResearch/lanfolk/internal/stun"
)

func sid(b byte) SessionID {
	var s SessionID
	for i := range s {
		s[i] = b + byte(i)
	}
	return s
}

func TestClassify(t *testing.T) {
	wgInit := make([]byte, 148)
	wgInit[0] = 1
	wgData := make([]byte, 64)
	wgData[0] = 4
	badWG := make([]byte, 64)
	badWG[0] = 4
	badWG[2] = 1
	cases := []struct {
		name string
		b    []byte
		want Class
	}{
		{"stun", stun.BindingRequest(stun.NewTxID()), ClassSTUN},
		{"wg-init", wgInit, ClassWireGuard},
		{"wg-data", wgData, ClassWireGuard},
		{"wg-bad-reserved", badWG, ClassUnknown},
		{"wg-short-init", wgInit[:100], ClassUnknown},
		{"relay-control", AppendPing(nil, 7), ClassRelayControl},
		{"relay-data", AppendRelayData(nil, sid(1), []byte{1, 2, 3}), ClassRelayData},
		{"control", append([]byte{KindControl}, make([]byte, 80)...), ClassControl},
		{"member-forward", AppendMemberForward(nil, sid(1), [8]byte{1}, []byte{9}), ClassMemberForward},
		{"member-deliver", AppendMemberDeliver(nil, sid(1), [8]byte{1}, []byte{9}), ClassMemberDeliver},
		{"empty", nil, ClassUnknown},
		{"dht", []byte("d1:ad2:id20:"), ClassUnknown},
	}
	for _, c := range cases {
		if got := Classify(c.b); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestRelayControlRoundTrip(t *testing.T) {
	s := sid(3)
	tok := Token{1, 2, 3, 4, 5, 6, 7, 8}
	m, err := ParseRelayControl(AppendRegister(nil, s, tok))
	if err != nil || m.Type != RelayRegister || m.Session != s || m.Token != tok {
		t.Fatalf("register: %+v %v", m, err)
	}
	addr := netip.MustParseAddrPort("198.51.100.4:6000")
	m, err = ParseRelayControl(AppendRegisterAck(nil, s, true, addr))
	if err != nil || m.Type != RelayRegisterAck || !m.PeerPresent || m.Addr != addr {
		t.Fatalf("ack: %+v %v", m, err)
	}
	addr6 := netip.MustParseAddrPort("[2001:db8::5]:7000")
	m, err = ParseRelayControl(AppendPong(nil, 99, addr6))
	if err != nil || m.Type != RelayPong || m.Nonce != 99 || m.Addr != addr6 {
		t.Fatalf("pong: %+v %v", m, err)
	}
	m, err = ParseRelayControl(AppendReject(nil, s, RejectSessionLimit))
	if err != nil || m.Type != RelayReject || m.Code != RejectSessionLimit {
		t.Fatalf("reject: %+v %v", m, err)
	}
	m, err = ParseRelayControl(AppendPeerJoined(nil, s))
	if err != nil || m.Type != RelayPeerJoined || m.Session != s {
		t.Fatalf("joined: %+v %v", m, err)
	}
	m, err = ParseRelayControl(AppendEchoRequest(nil, 5, 30))
	if err != nil || m.Type != RelayEchoRequest || m.Nonce != 5 || m.DelaySec != 30 {
		t.Fatalf("echo: %+v %v", m, err)
	}
	m, err = ParseRelayControl(AppendUnregister(nil, s, tok))
	if err != nil || m.Type != RelayUnregister || m.Token != tok {
		t.Fatalf("unregister: %+v %v", m, err)
	}
	if _, err := ParseRelayControl([]byte{KindRelayControl, 9, RelayPing, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("unsupported version accepted")
	}
	if _, err := ParseRelayControl(AppendRegister(nil, s, tok)[:10]); err == nil {
		t.Fatal("truncated register accepted")
	}
}

func TestRelayDataRoundTrip(t *testing.T) {
	s := sid(9)
	payload := []byte("hello")
	f := AppendRelayData(nil, s, payload)
	if len(f) != RelayDataOverhead+len(payload) {
		t.Fatalf("overhead %d", len(f)-len(payload))
	}
	gs, gp, ok := ParseRelayData(f)
	if !ok || gs != s || !bytes.Equal(gp, payload) {
		t.Fatal("relay data round trip")
	}
}

func TestMemberFrames(t *testing.T) {
	s := sid(2)
	id := [8]byte{9, 8, 7, 6, 5, 4, 3, 2}
	f := AppendMemberForward(nil, s, id, []byte("x"))
	if len(f) != MemberRelayOverhead+1 {
		t.Fatalf("overhead %d", len(f)-1)
	}
	gs, gid, p, ok := ParseMemberFrame(f)
	if !ok || gs != s || gid != id || string(p) != "x" {
		t.Fatal("member frame round trip")
	}
}

func TestControlSealOpen(t *testing.T) {
	var key [32]byte
	key[0] = 42
	s := sid(5)
	plain := []byte("candidates")
	f := SealControl(nil, &key, s, plain)
	if Classify(f) != ClassControl {
		t.Fatal("sealed frame misclassified")
	}
	gs, ok := ControlSession(f)
	if !ok || gs != s {
		t.Fatal("session extraction")
	}
	got, err := OpenControl(&key, f)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %v", err)
	}
	f[len(f)-1] ^= 1
	if _, err := OpenControl(&key, f); err == nil {
		t.Fatal("tampered frame opened")
	}
	other := key
	other[1] = 1
	f[len(f)-1] ^= 1
	if _, err := OpenControl(&other, f); err == nil {
		t.Fatal("wrong key opened frame")
	}
}

func TestMTUBudget(t *testing.T) {
	// PPPoE 1492 - IPv6 40 - UDP 8 - WireGuard 32 - relay header must fit
	// a full 1380-byte inner packet on every relay path.
	for name, overhead := range map[string]int{"shared": RelayDataOverhead, "member": MemberRelayOverhead} {
		if 1380+32+overhead+48 > 1492 {
			t.Errorf("%s relay overhead %d breaks the MTU budget", name, overhead)
		}
	}
}
