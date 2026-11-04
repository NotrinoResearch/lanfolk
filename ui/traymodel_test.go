package main

import (
	"strings"
	"testing"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

func testNetworks() []ipc.Network {
	return []ipc.Network{
		{ID: "n1", Name: "Friday LAN", IsOwner: true, Active: true, Members: []ipc.Member{
			{PublicKey: "me", Nickname: "Ahmet", VirtualIP: "10.77.0.2", IsSelf: true, IsOwner: true},
			{PublicKey: "k2", Nickname: "Mehmet", VirtualIP: "10.77.0.3"},
			{PublicKey: "k3", Nickname: "Veli", VirtualIP: "10.77.0.4"},
		}},
		{ID: "n2", Name: "Lunch group", Members: []ipc.Member{{PublicKey: "me", IsSelf: true}}},
	}
}

func upStatus() *ipc.Status {
	return &ipc.Status{Nickname: "Ahmet", ActiveNetworkID: "n1", ActiveNetworkName: "Friday LAN",
		VirtualIP: "10.77.0.2", AdapterUp: true,
		Relays: []ipc.RelayStatus{{Name: "relay:3478", Source: "default", Mode: "udp", RTTMs: 40}}}
}

func TestTrayViewServiceDown(t *testing.T) {
	v := trayView(traySnapshot{})
	if v.Icon != trayAttention {
		t.Fatalf("icon = %q, want attention", v.Icon)
	}
	if !strings.Contains(v.Status, "service is not running") {
		t.Fatalf("status = %q", v.Status)
	}
	if v.IP != "" || len(v.Networks) != 0 || v.CanInvite {
		t.Fatalf("service down must not offer network actions: %+v", v)
	}
}

func TestTrayViewNoActiveNetwork(t *testing.T) {
	st := upStatus()
	st.ActiveNetworkID, st.ActiveNetworkName, st.VirtualIP, st.AdapterUp = "", "", "", false
	nets := testNetworks()
	nets[0].Active = false
	v := trayView(traySnapshot{Connected: true, Status: st, Networks: nets})
	if v.Icon != trayOffline {
		t.Fatalf("icon = %q, want offline", v.Icon)
	}
	if v.Status != "Not connected to a network" {
		t.Fatalf("status = %q", v.Status)
	}
	if len(v.Networks) != 2 || v.Networks[0].Active || v.Networks[1].Active {
		t.Fatalf("networks = %+v", v.Networks)
	}
	if v.CanInvite {
		t.Fatal("no active network: nothing to invite to")
	}
}

func TestTrayViewOnline(t *testing.T) {
	peers := []ipc.Peer{
		{PublicKey: "k2", Online: true, Path: "direct", PingMs: 18},
		{PublicKey: "k3", Online: false, Path: "none"},
	}
	v := trayView(traySnapshot{Connected: true, Status: upStatus(), Networks: testNetworks(), Peers: peers})
	if v.Icon != trayOnline {
		t.Fatalf("icon = %q, want online", v.Icon)
	}
	if v.Status != "Friday LAN · 2 of 3 online" {
		t.Fatalf("status = %q", v.Status)
	}
	if v.IP != "10.77.0.2" {
		t.Fatalf("ip = %q", v.IP)
	}
	if !v.CanInvite || v.ActiveNetworkID != "n1" {
		t.Fatalf("owner of the active network can invite: %+v", v)
	}
	if !v.Networks[0].Active || v.Networks[1].Active {
		t.Fatalf("networks = %+v", v.Networks)
	}
	if !strings.HasPrefix(v.Tooltip, "Lanfolk") || !strings.Contains(v.Tooltip, "2 of 3 online") {
		t.Fatalf("tooltip = %q", v.Tooltip)
	}
}

func TestTrayViewAloneInNetwork(t *testing.T) {
	nets := testNetworks()
	nets[0].Members = nets[0].Members[:1]
	v := trayView(traySnapshot{Connected: true, Status: upStatus(), Networks: nets})
	if v.Icon != trayOnline || v.Status != "Friday LAN · waiting for friends" {
		t.Fatalf("got %q %q", v.Icon, v.Status)
	}
}

func TestTrayViewConnecting(t *testing.T) {
	// Adapter not up yet.
	st := upStatus()
	st.AdapterUp = false
	v := trayView(traySnapshot{Connected: true, Status: st, Networks: testNetworks()})
	if v.Icon != trayConnecting {
		t.Fatalf("adapter down: icon = %q, want connecting", v.Icon)
	}
	// No relay and nobody online yet.
	st = upStatus()
	st.Relays[0].Mode = "down"
	v = trayView(traySnapshot{Connected: true, Status: st, Networks: testNetworks(),
		Peers: []ipc.Peer{{PublicKey: "k2"}, {PublicKey: "k3"}}})
	if v.Icon != trayConnecting {
		t.Fatalf("relays down, nobody online: icon = %q, want connecting", v.Icon)
	}
}

func TestTrayViewAdapterError(t *testing.T) {
	st := upStatus()
	st.AdapterUp = false
	st.AdapterError = "Wintun could not be loaded"
	v := trayView(traySnapshot{Connected: true, Status: st, Networks: testNetworks()})
	if v.Icon != trayAttention || !strings.Contains(v.Status, "adapter") {
		t.Fatalf("got %q %q", v.Icon, v.Status)
	}
}

func TestTrayViewMemberCannotInvite(t *testing.T) {
	nets := testNetworks()
	nets[0].IsOwner = false
	v := trayView(traySnapshot{Connected: true, Status: upStatus(), Networks: nets})
	if v.CanInvite {
		t.Fatal("only the owner can create invites")
	}
}

func TestTrayTooltipLength(t *testing.T) {
	nets := testNetworks()
	nets[0].Name = strings.Repeat("Very long network name ", 10)
	st := upStatus()
	st.ActiveNetworkName = nets[0].Name
	v := trayView(traySnapshot{Connected: true, Status: st, Networks: nets})
	// NOTIFYICONDATA.szTip holds 128 UTF-16 units including the terminator.
	if n := len([]rune(v.Tooltip)); n > 127 {
		t.Fatalf("tooltip has %d characters", n)
	}
}
