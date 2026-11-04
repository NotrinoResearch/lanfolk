package wire

import (
	"bytes"
	"net/netip"
	"testing"
)

// FuzzParsers feeds arbitrary datagrams to every parser that sees untrusted
// network input on the shared socket. None may panic, and successful parses
// must round-trip.
func FuzzParsers(f *testing.F) {
	f.Add(AppendRegister(nil, SessionID{1}, Token{2}))
	f.Add(AppendPong(nil, 9, netip.MustParseAddrPort("198.51.100.1:5")))
	f.Add(AppendRelayData(nil, SessionID{3}, []byte("x")))
	f.Add(AppendMemberForward(nil, SessionID{4}, [8]byte{5}, []byte("y")))
	f.Add([]byte{0xF2})
	f.Fuzz(func(t *testing.T, b []byte) {
		Classify(b)
		if m, err := ParseRelayControl(b); err == nil {
			_ = m
		}
		if sid, p, ok := ParseRelayData(b); ok {
			if !bytes.Equal(AppendRelayData(nil, sid, p), b) {
				t.Fatal("relay data does not round-trip")
			}
		}
		ParseMemberFrame(b)
		ControlSession(b)
		var key [32]byte
		OpenControl(&key, b)
		ParseAddr(b)
		ReadStreamFrame(bytes.NewReader(b), make([]byte, MaxStreamFrame))
	})
}
