package engine

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

func TestRedactAddr(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77":        "203.0.x.x",
		"88.240.12.34":        "88.240.x.x",
		"::ffff:88.240.12.34": "88.240.x.x",
		"2a02:4780:12:34::1":  "2a02:4780:x::x",
		"192.168.1.20":        "192.168.1.20",
		"10.77.0.3":           "10.77.0.3",
		"172.16.5.4":          "172.16.5.4",
		"100.72.1.2":          "100.72.1.2", // carrier NAT
		"127.0.0.1":           "127.0.0.1",
		"169.254.1.1":         "169.254.1.1",
		"fe80::1":             "fe80::1",
		"fd00::5":             "fd00::5",
		"0.0.0.0":             "0.0.0.0",
		"239.255.255.250":     "239.255.255.250",
	} {
		if got := redactAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("redactAddr(%s) = %s, want %s", in, got, want)
		}
	}
	if got := redactAddrPort(netip.MustParseAddrPort("203.0.113.77:41641")); got != "203.0.x.x:41641" {
		t.Errorf("redactAddrPort = %s", got)
	}
	if got := redactAddrPort(netip.MustParseAddrPort("[2a02:4780:12:34::1]:3478")); got != "[2a02:4780:x::x]:3478" {
		t.Errorf("redactAddrPort v6 = %s", got)
	}
	if got := redactAddrPort(netip.AddrPort{}); got != "" {
		t.Errorf("zero = %q", got)
	}
}

func TestRedactHostPortKeepsNames(t *testing.T) {
	for in, want := range map[string]string{
		"lanfolk-relay1.notrino.com:3478": "lanfolk-relay1.notrino.com:3478",
		"relay.example.org":               "relay.example.org",
		"203.0.113.10:3478":               "203.0.x.x:3478",
		"203.0.113.10":                    "203.0.x.x",
	} {
		if got := redactHostPort(in); got != want {
			t.Errorf("redactHostPort(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestRedactStatus(t *testing.T) {
	st := ipc.Status{
		PublicKey:    strings.Repeat("ab", 32),
		FriendCode:   "lanfolk1abcdef",
		Relays:       []ipc.RelayStatus{{Name: "203.0.113.10:3478"}, {Name: "lanfolk-relay1.notrino.com:3478"}},
		Warnings:     []string{"port mapping failed for 88.240.12.34:41641 (gateway 192.168.1.1)"},
		AdapterError: "",
	}
	redactStatus(&st)
	if st.PublicKey != "abababab…" || st.FriendCode != "(hidden)" {
		t.Errorf("identity not redacted: %q %q", st.PublicKey, st.FriendCode)
	}
	if st.Relays[0].Name != "203.0.x.x:3478" || st.Relays[1].Name != "lanfolk-relay1.notrino.com:3478" {
		t.Errorf("relays = %+v", st.Relays)
	}
	if w := st.Warnings[0]; strings.Contains(w, "88.240.12.34") || !strings.Contains(w, "88.240.x.x:41641") || !strings.Contains(w, "192.168.1.1") {
		t.Errorf("warning = %q", w)
	}
}

// The real report never carries the full key or the friend code of anyone.
func TestDiagnosticsReportIsRedacted(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "a", r), newNode(t, "b", r)
	befriend(t, a, b)
	createNetwork(t, a, b)
	rep := a.e.Diagnostics()
	for _, secret := range []string{a.code(), b.code(), a.e.PublicKey().Hex(), b.e.PublicKey().Hex()} {
		if strings.Contains(rep, secret) {
			t.Fatalf("report contains %q:\n%s", secret, rep)
		}
	}
	if !strings.Contains(rep, b.e.PublicKey().String()) {
		t.Errorf("report lacks the peer's key prefix:\n%s", rep)
	}
}
