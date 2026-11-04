package network

import (
	"testing"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
)

func FuzzUnmarshal(f *testing.F) {
	k, _ := identity.Generate()
	l, _ := New(k, "owner", "net")
	l.SetRelays(k, []Relay{{Host: "relay.example.org", UDPPort: 3478}})
	f.Add(l.Marshal())
	f.Fuzz(func(t *testing.T, b []byte) {
		if l, err := Unmarshal(b); err == nil {
			if err := l.Verify(); err != nil {
				t.Fatal("accepted list does not verify")
			}
		}
	})
}
