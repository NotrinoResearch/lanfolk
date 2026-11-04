package peermsg

import (
	"bytes"
	"crypto/rand"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
)

func roundTrip(t *testing.T, msg Message) Message {
	t.Helper()
	h := Header{Boot: 11, Counter: 22}
	b := Marshal(h, msg)
	if len(b) > MaxPlaintext {
		t.Fatalf("%T encodes to %d bytes", msg, len(b))
	}
	gh, got, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("%T: %v", msg, err)
	}
	if gh.Boot != 11 || gh.Counter != 22 || gh.Type != msg.Type() {
		t.Fatalf("header %+v", gh)
	}
	return got
}

func TestRoundTrips(t *testing.T) {
	var nid network.ID
	nid[0] = 5
	msgs := []Message{
		&Hello{ProtocolVersion: 1, Flags: FlagRelayOnly, Nickname: "Mehmet", Capabilities: CapMemberRelay,
			ActiveNetwork: nid, Networks: []NetRef{{ID: nid, Version: 7}}},
		&MembershipUpdate{MsgID: 9, Index: 1, Total: 3, Data: []byte("chunk")},
		&Candidates{Gen: 4, Addrs: []Candidate{
			{Source: 3, Addr: netip.MustParseAddrPort("203.0.113.5:4444")},
			{Source: 2, Addr: netip.MustParseAddrPort("[2001:db8::1]:4444")},
		}},
		&PathProbe{ProbeID: 1 << 60, PathID: 3},
		&PathProbeAck{ProbeID: 99, PathID: 3, Observed: netip.MustParseAddrPort("198.51.100.2:5555")},
		&RelayOffer{Entries: []OfferEntry{{Peer: identity.KeyID{1, 2}, RTT: 12 * time.Millisecond}}},
		&Bye{},
		&JoinRequest{Network: nid, Joiner: identity.PublicKey{7, 8}, Timestamp: 1_800_000_000, Nickname: "Mehmet", Signature: [64]byte{1, 2, 3}},
		&JoinAccepted{Network: nid, OwnerNickname: "Ahmet", NetworkName: "Friday games", Signature: [64]byte{9, 8, 7}},
	}
	for _, m := range msgs {
		got := roundTrip(t, m)
		if !reflect.DeepEqual(got, m) {
			t.Errorf("%T: got %+v want %+v", m, got, m)
		}
	}
}

func TestLimitsFitOneDatagram(t *testing.T) {
	c := &Candidates{}
	for i := 0; i < MaxCandidates+10; i++ {
		c.Addrs = append(c.Addrs, Candidate{Source: 1, Addr: netip.MustParseAddrPort("[2001:db8::1]:1")})
	}
	got := roundTrip(t, c).(*Candidates)
	if len(got.Addrs) != MaxCandidates {
		t.Fatalf("candidates %d", len(got.Addrs))
	}
	o := &RelayOffer{}
	for i := 0; i < MaxOfferEntries+10; i++ {
		o.Entries = append(o.Entries, OfferEntry{RTT: time.Millisecond})
	}
	if n := len(roundTrip(t, o).(*RelayOffer).Entries); n != MaxOfferEntries {
		t.Fatalf("offers %d", n)
	}
	h := &Hello{Nickname: string(bytes.Repeat([]byte("x"), 100))}
	for i := 0; i < 100; i++ {
		h.Networks = append(h.Networks, NetRef{Version: uint64(i)})
	}
	gh := roundTrip(t, h).(*Hello)
	if len(gh.Nickname) != network.MaxNicknameLen || len(gh.Networks) != MaxNetRefs {
		t.Fatalf("hello limits: %d %d", len(gh.Nickname), len(gh.Networks))
	}
}

func TestRejectsGarbage(t *testing.T) {
	for i := 0; i < 2000; i++ {
		b := make([]byte, i%64)
		rand.Read(b)
		Unmarshal(b) // must not panic
	}
	if _, _, err := Unmarshal(Marshal(Header{}, &PathProbe{})[:20]); err == nil {
		t.Fatal("truncated probe accepted")
	}
}

func TestFragmentAndReassemble(t *testing.T) {
	data := make([]byte, 17000) // about a full 253-member list
	rand.Read(data)
	frags := Fragment(data, 42)
	if len(frags) < 2 {
		t.Fatal("not fragmented")
	}
	for _, f := range frags {
		if n := len(Marshal(Header{}, f)); n > MaxPlaintext {
			t.Fatalf("fragment encodes to %d bytes", n)
		}
	}
	r := NewReassembler(5 * time.Second)
	now := time.Now()
	// Out of order and with a duplicate.
	order := append([]*MembershipUpdate{frags[len(frags)-1]}, frags...)
	var out []byte
	for _, f := range order {
		if done := r.Add(f, now); done != nil {
			out = done
		}
	}
	if !bytes.Equal(out, data) {
		t.Fatal("reassembly mismatch")
	}
	// A lost fragment expires.
	r.Add(frags[0], now)
	if r.Pending() != 1 {
		t.Fatal("pending")
	}
	r.Add(&MembershipUpdate{MsgID: 77, Index: 0, Total: 2, Data: []byte("a")}, now.Add(10*time.Second))
	if r.Pending() != 1 {
		t.Fatal("expired message not dropped")
	}
}

func TestReassemblerRejectsInconsistentTotals(t *testing.T) {
	r := NewReassembler(time.Second)
	now := time.Now()
	r.Add(&MembershipUpdate{MsgID: 1, Index: 0, Total: 3, Data: []byte("a")}, now)
	if r.Add(&MembershipUpdate{MsgID: 1, Index: 1, Total: 2, Data: []byte("b")}, now) != nil {
		t.Fatal("inconsistent total accepted")
	}
	if r.Add(&MembershipUpdate{MsgID: 2, Index: 5, Total: 2, Data: []byte("b")}, now) != nil {
		t.Fatal("index beyond total accepted")
	}
}

func TestJoinMessageLimits(t *testing.T) {
	long := string(bytes.Repeat([]byte("x"), 200))
	r := roundTrip(t, &JoinRequest{Nickname: long}).(*JoinRequest)
	if len(r.Nickname) != network.MaxNicknameLen {
		t.Fatalf("nickname %d", len(r.Nickname))
	}
	a := roundTrip(t, &JoinAccepted{OwnerNickname: long, NetworkName: long}).(*JoinAccepted)
	if len(a.OwnerNickname) != network.MaxNicknameLen || len(a.NetworkName) != network.MaxNameLen {
		t.Fatalf("accepted limits: %d %d", len(a.OwnerNickname), len(a.NetworkName))
	}
	// A truncated body is invalid, and so is text that is too long on the wire.
	b := Marshal(Header{}, &JoinRequest{Nickname: "a"})
	for n := HeaderLen; n < len(b); n++ {
		if _, _, err := Unmarshal(b[:n]); err == nil {
			t.Fatalf("join request truncated to %d bytes accepted", n)
		}
	}
	bad := Marshal(Header{}, &JoinAccepted{})
	bad = append(bad[:HeaderLen+16], append([]byte{200}, bytes.Repeat([]byte("y"), 200)...)...)
	if _, _, err := Unmarshal(bad); err == nil {
		t.Fatal("over-long owner nickname accepted")
	}
}
