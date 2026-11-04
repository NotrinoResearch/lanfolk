package peermsg

import (
	"testing"
	"time"
)

func FuzzUnmarshal(f *testing.F) {
	f.Add(Marshal(Header{Boot: 1, Counter: 2}, &Hello{Nickname: "a"}))
	f.Add(Marshal(Header{}, &Candidates{}))
	f.Add(Marshal(Header{}, &MembershipUpdate{Total: 2, Data: []byte("x")}))
	f.Add(Marshal(Header{}, &RelayOffer{Entries: []OfferEntry{{RTT: time.Millisecond}}}))
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
