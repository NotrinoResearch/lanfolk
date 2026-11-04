//go:build !nomock

package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// Invites in the -mock preview: a redeemed code waits a few seconds for
// its "owner", then turns into a network.

type mockInvites struct {
	joins []ipc.PendingJoin
}

func (m *mockService) mockInviteCode(netID string, reset bool) ipc.InviteResult {
	suffix := "yb8fqk3p1ctwm7rz9ah4ea6g5dqxo8t3bkhcj1zr7snf4wy9uiep3xm6dgcb1nhqk8a5ojtr3zcw7sf9yeu4dpxhbig6kq1mn8ar5tcz3w"
	if reset {
		suffix = strings.Repeat("q", 10) + suffix[10:]
	}
	return ipc.InviteResult{Code: "lanfolk-i1" + suffix, Expires: time.Now().Add(7 * 24 * time.Hour)}
}

// dispatchInvite handles the invites.* methods; ok is false for others.
func (m *mockService) dispatchInvite(method string, params json.RawMessage) (any, bool, error) {
	switch method {
	case ipc.MethodCreateInvite:
		var p ipc.InviteParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, true, err
		}
		if _, err := m.ownedNetwork(p.NetworkID); err != nil {
			return nil, true, err
		}
		return m.mockInviteCode(p.NetworkID, p.Reset), true, nil
	case ipc.MethodRedeemInvite:
		var p ipc.CodeParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, true, err
		}
		if !strings.HasPrefix(p.Code, "lanfolk-i1") || len(p.Code) < 40 {
			return nil, true, ipc.Errorf(ipc.CodeInvalid, "that invite code is not valid; check that it was copied completely")
		}
		id := "net-joined-" + time.Now().Format("150405")
		j := ipc.PendingJoin{NetworkID: id, OwnerPublicKey: "key-burak", OwnerNickname: "Burak",
			State: ipc.JoinWaiting, Expires: time.Now().Add(7 * 24 * time.Hour)}
		m.inv.joins = append(m.inv.joins, j)
		m.publishInvites()
		go m.completeJoin(id)
		return ipc.RedeemResult{NetworkID: id, OwnerPublicKey: j.OwnerPublicKey, State: j.State}, true, nil
	case ipc.MethodListInvites:
		return append([]ipc.PendingJoin{}, m.inv.joins...), true, nil
	case ipc.MethodCancelInvite:
		var p ipc.NetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, true, err
		}
		out := m.inv.joins[:0]
		for _, j := range m.inv.joins {
			if j.NetworkID != p.NetworkID {
				out = append(out, j)
			}
		}
		m.inv.joins = out
		m.publishInvites()
		return true, true, nil
	}
	return nil, false, nil
}

func (m *mockService) completeJoin(id string) {
	time.Sleep(4 * time.Second)
	m.mu.Lock()
	found := false
	for i := range m.inv.joins {
		if m.inv.joins[i].NetworkID == id {
			m.inv.joins[i].State = ipc.JoinJoined
			m.inv.joins[i].NetworkName = "Burak's LAN"
			found = true
		}
	}
	if found {
		m.networks = append(m.networks, ipc.Network{ID: id, Name: "Burak's LAN", OwnerKey: "key-burak", Version: 1,
			Members: []ipc.Member{
				{PublicKey: "key-burak", Nickname: "Burak", VirtualIP: "10.77.0.2", IsOwner: true},
				{PublicKey: mockSelfKey, Nickname: m.nickname, VirtualIP: "10.77.0.3", IsSelf: true},
			}})
		m.publishAll()
		m.publishInvites()
	}
	m.mu.Unlock()
}

// publishInvites must be called with m.mu held.
func (m *mockService) publishInvites() {
	b, _ := json.Marshal(append([]ipc.PendingJoin{}, m.inv.joins...))
	go m.srv.Publish(ipc.EventInvites, json.RawMessage(b))
}
