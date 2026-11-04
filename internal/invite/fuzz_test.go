package invite

import (
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
)

// FuzzParse feeds arbitrary text to the code parser. It must never panic, and
// a code it accepts must survive being formatted and parsed again.
func FuzzParse(f *testing.F) {
	now := time.Unix(1_800_000_000, 0)
	k, _ := identity.Generate()
	c, _ := New(k.Public(), network.ID{9}, TTL, now)
	f.Add(c.String())
	f.Add(Prefix)
	f.Add("lanfolk-i1 yyyy-yyyy")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := Parse(s, now)
		if err != nil {
			return
		}
		again, err := Parse(got.String(), now)
		if err != nil || again != got {
			t.Fatalf("accepted code does not round-trip: %v", err)
		}
	})
}
