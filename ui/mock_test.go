//go:build !nomock

package main

import (
	"context"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// The -mock service must start and answer every method the App uses, so a
// preview never fails on startup.
func TestMockServiceServesTheWholeApp(t *testing.T) {
	stop, addr, err := startMock()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	h := &fakeHost{}
	a := fastApp(addr, h)
	a.start(context.Background())
	defer a.stop()
	h.wait(t, "service", serviceIs(true))

	st, err := a.Status()
	if err != nil || st.Nickname == "" || len(st.FriendCode) != 66 {
		t.Fatalf("status %+v %v", st, err)
	}
	friends, err := a.ListFriends()
	if err != nil || len(friends) < 3 {
		t.Fatalf("friends %v %v", friends, err)
	}
	nets, err := a.ListNetworks()
	if err != nil || len(nets) < 2 {
		t.Fatalf("networks %v %v", nets, err)
	}
	if _, err := a.GetSettings(); err != nil {
		t.Fatal(err)
	}
	peers, err := a.ListPeers()
	if err != nil || len(peers) == 0 {
		t.Fatalf("peers %v %v", peers, err)
	}

	n, err := a.CreateNetwork("Test net")
	if err != nil || n == nil {
		t.Fatalf("create: %v %v", n, err)
	}
	if err := a.AddMember(n.ID, "key-mehmet"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddMember(n.ID, "key-can"); err == nil {
		t.Fatal("a pending friend was added to a network")
	}
	if err := a.RenameNetwork(n.ID, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNetworkRelays(n.ID, []ipc.Relay{{Host: "r.example", UDPPort: 3478, TCPPort: 443}}); err != nil {
		t.Fatal(err)
	}
	if err := a.ActivateNetwork(n.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ActivateNetwork(""); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveMember(n.ID, "key-mehmet"); err != nil {
		t.Fatal(err)
	}
	if err := a.LeaveNetwork(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFriend("lanfolk1"+"abcdefghijklmnopqrstuvwxyz", "New"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFriend("nonsense", ""); err == nil {
		t.Fatal("invalid code accepted")
	}
	if _, err := a.CreateConnCode("key-veli"); err != nil {
		t.Fatal(err)
	}
	if err := a.UseConnCode("LFC-1234"); err != nil {
		t.Fatal(err)
	}
	if b, err := a.ExportIdentity("password1"); err != nil || b == "" {
		t.Fatalf("%q %v", b, err)
	}
	if err := a.SetSettings(ipc.Settings{DHTFallback: true}); err != nil {
		t.Fatal(err)
	}
	// Events flow: creating a network publishes a networks event.
	h.wait(t, "networks", func(d any) bool { n, ok := d.([]ipc.Network); return ok && len(n) >= 2 })
	// The simulation publishes peers while a network is active.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.names()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
