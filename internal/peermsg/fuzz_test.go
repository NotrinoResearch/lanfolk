package peermsg

import (
	"reflect"
	"testing"
	"time"
)

func FuzzUnmarshal(f *testing.F) {
	f.Add(Marshal(Header{Boot: 1, Counter: 2}, &Hello{Nickname: "a"}))
	f.Add(Marshal(Header{}, &Candidates{}))
	f.Add(Marshal(Header{}, &MembershipUpdate{Total: 2, Data: []byte("x")}))
	f.Add(Marshal(Header{}, &RelayOffer{Entries: []OfferEntry{{RTT: time.Millisecond}}}))
	f.Add(Marshal(Header{}, &JoinRequest{Nickname: "a", Timestamp: 5}))
	f.Add(Marshal(Header{}, &JoinAccepted{OwnerNickname: "a", NetworkName: "b"}))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, m, err := Unmarshal(b)
		if err != nil {
			return
		}
		// A decoded message re-encodes and decodes to the same type.
		if _, m2, err := Unmarshal(Marshal(h, m)); err != nil || m2.Type() != m.Type() {
			t.Fatalf("re-encode failed: %v", err)
		}
		if u, ok := m.(*MembershipUpdate); ok {
			NewReassembler(time.Second).Add(u, time.Now())
		}
	})
}

// FuzzJoinMessages decodes arbitrary bytes as the two invite messages, the
// only control messages that arrive from someone who merely holds an invite
// code. Decoding must not panic and accepted bodies must round-trip.
func FuzzJoinMessages(f *testing.F) {
	f.Add(Marshal(Header{Boot: 1, Counter: 2}, &JoinRequest{Nickname: "n", Timestamp: 99, Signature: [64]byte{1}}))
	f.Add(Marshal(Header{}, &JoinAccepted{OwnerNickname: "o", NetworkName: "net"}))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		h, m, err := Unmarshal(b)
		if err != nil {
			return
		}
		switch m.(type) {
		case *JoinRequest, *JoinAccepted:
			_, m2, err := Unmarshal(Marshal(h, m))
			if err != nil || !reflect.DeepEqual(m, m2) {
				t.Fatalf("join message does not round-trip: %v", err)
			}
		}
	})
}
