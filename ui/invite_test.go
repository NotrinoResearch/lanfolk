package main

import (
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

func TestInviteCallsAreForwarded(t *testing.T) {
	exp := time.Unix(1800000000, 0).UTC()
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodCreateInvite: ipc.InviteResult{Code: "lanfolk-i1abc", Expires: exp},
		ipc.MethodRedeemInvite: ipc.RedeemResult{NetworkID: "n9", OwnerPublicKey: "OWNER", State: ipc.JoinWaiting},
		ipc.MethodListInvites:  []ipc.PendingJoin{{NetworkID: "n9", State: ipc.JoinWaiting, OwnerNickname: "Zeynep"}},
	})
	a, _ := startedApp(t, f)

	inv, err := a.CreateInvite("n1", false)
	if err != nil || inv.Code != "lanfolk-i1abc" || !inv.Expires.Equal(exp) {
		t.Fatalf("CreateInvite: %v %+v", err, inv)
	}
	jsonEq(t, f.last(t, ipc.MethodCreateInvite).Params, `{"network_id":"n1"}`)
	if _, err := a.CreateInvite("n1", true); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, f.last(t, ipc.MethodCreateInvite).Params, `{"network_id":"n1","reset":true}`)

	r, err := a.RedeemInvite("  lanfolk-i1abc \n")
	if err != nil || r.NetworkID != "n9" || r.State != ipc.JoinWaiting {
		t.Fatalf("RedeemInvite: %v %+v", err, r)
	}
	// Pasted codes are trimmed before they reach the service.
	jsonEq(t, f.last(t, ipc.MethodRedeemInvite).Params, `{"code":"lanfolk-i1abc"}`)

	list, err := a.ListInvites()
	if err != nil || len(list) != 1 || list[0].OwnerNickname != "Zeynep" {
		t.Fatalf("ListInvites: %v %+v", err, list)
	}
	if err := a.CancelInvite("n9"); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, f.last(t, ipc.MethodCancelInvite).Params, `{"network_id":"n9"}`)
}

func TestInvitesEventAndSnapshot(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodListInvites: []ipc.PendingJoin{{NetworkID: "snap", State: ipc.JoinJoining}},
	})
	_, h := startedApp(t, f)
	h.wait(t, ipc.EventInvites, func(d any) bool {
		p, ok := d.([]ipc.PendingJoin)
		return ok && len(p) == 1 && p[0].NetworkID == "snap"
	})
	f.srv.Publish(ipc.EventInvites, []ipc.PendingJoin{{NetworkID: "pushed", State: ipc.JoinJoined}})
	h.wait(t, ipc.EventInvites, func(d any) bool {
		p, ok := d.([]ipc.PendingJoin)
		return ok && len(p) == 1 && p[0].NetworkID == "pushed"
	})
	// A nil list arrives as an empty list.
	f.srv.Publish(ipc.EventInvites, nil)
	h.wait(t, ipc.EventInvites, func(d any) bool { p, ok := d.([]ipc.PendingJoin); return ok && p != nil && len(p) == 0 })
}

func TestTeeHostCopiesEvents(t *testing.T) {
	h := &fakeHost{}
	var got []string
	tee := teeHost{Host: h, also: func(event string, _ any) { got = append(got, event) }}
	tee.Emit("status", 1)
	tee.Emit("peers", 2)
	if len(got) != 2 || got[0] != "status" || got[1] != "peers" {
		t.Fatalf("observer saw %v", got)
	}
	if n := h.names(); len(n) != 2 {
		t.Fatalf("host saw %v", n)
	}
}

// The frontend registers its listeners after Wails' OnStartup, so events
// sent before the page loaded are lost. Resync sends the current state again.
func TestResyncReemitsState(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodStatus:       ipc.Status{Version: "resync"},
		ipc.MethodListNetworks: []ipc.Network{{ID: "n"}},
	})
	a, h := startedApp(t, f)
	h.wait(t, ipc.EventStatus, nil)
	h.clear()
	a.Resync()
	h.wait(t, eventService, serviceIs(true))
	h.wait(t, ipc.EventStatus, func(d any) bool { s, ok := d.(ipc.Status); return ok && s.Version == "resync" })
	h.wait(t, ipc.EventNetworks, nil)

	// Without a service, Resync still tells the page that it is not running.
	f.stop()
	h.wait(t, eventService, serviceIs(false))
	h.clear()
	a.Resync()
	h.wait(t, eventService, serviceIs(false))
}
