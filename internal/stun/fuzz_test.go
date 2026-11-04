package stun

import (
	"net/netip"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add(BindingRequest(NewTxID()))
	f.Add(BindingResponse(NewTxID(), netip.MustParseAddrPort("[2001:db8::1]:9")))
	f.Fuzz(func(t *testing.T, b []byte) {
		IsMessage(b)
		ParseBindingRequest(b)
		ParseBindingResponse(b)
	})
}
