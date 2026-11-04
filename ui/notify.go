package main

import (
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// Windows notifications (PRODUCT.md principle 4: quiet by default, a
// notification when something changes). The rules live here, free of any
// window code, so they can be tested; tray_windows.go shows the result.

// notice is one notification.
type notice struct {
	Title string
	Body  string
}

const (
	// noticeMinOffline: a member who was gone for less is not news (a
	// router restart, a Wi-Fi blip).
	noticeMinOffline = 2 * time.Minute
	// noticeRepeat: at most one "is online" per member in this time.
	noticeRepeat = 10 * time.Minute
)

// noticeState remembers what the user has seen so that only changes are
// announced, never the state found at start.
type noticeState struct {
	network    string // active network the peer state belongs to
	peersReady bool
	online     map[string]bool
	offlineAt  map[string]time.Time // zero: offline since before we looked
	notifiedAt map[string]time.Time

	members      map[string]map[string]bool // network -> member keys
	membersReady bool

	joins      map[string]string // invite network -> state
	joinsReady bool

	update string // newest version already announced
}

func newNoticeState(announcedUpdate string) *noticeState {
	return &noticeState{online: map[string]bool{}, offlineAt: map[string]time.Time{},
		notifiedAt: map[string]time.Time{}, members: map[string]map[string]bool{},
		joins: map[string]string{}, update: announcedUpdate}
}

func activeNet(nets []ipc.Network) *ipc.Network {
	for i := range nets {
		if nets[i].Active {
			return &nets[i]
		}
	}
	return nil
}

// peers handles a peers snapshot of the active network.
func (n *noticeState) peers(nets []ipc.Network, peers []ipc.Peer, now time.Time) []notice {
	net := activeNet(nets)
	if net == nil {
		n.network, n.peersReady = "", false
		return nil
	}
	if net.ID != n.network {
		// Another network: start over without announcing anyone.
		n.network, n.peersReady = net.ID, false
		clear(n.online)
		clear(n.offlineAt)
	}
	var out []notice
	for _, p := range peers {
		was, known := n.online[p.PublicKey]
		switch {
		case p.Online && !was:
			if n.peersReady && (!known || now.Sub(n.offlineAt[p.PublicKey]) >= noticeMinOffline) &&
				!n.recentlyNotified(p.PublicKey, now) {
				n.notifiedAt[p.PublicKey] = now
				out = append(out, notice{Title: p.Nickname + " is online", Body: "In " + net.Name})
			}
		case !p.Online && (was || !known):
			if known {
				n.offlineAt[p.PublicKey] = now
			} else {
				n.offlineAt[p.PublicKey] = time.Time{}
			}
		}
		n.online[p.PublicKey] = p.Online
	}
	n.peersReady = true
	return out
}

func (n *noticeState) recentlyNotified(key string, now time.Time) bool {
	at, ok := n.notifiedAt[key]
	return ok && now.Sub(at) < noticeRepeat
}

// networks tells an owner who joined one of their networks.
func (n *noticeState) networks(nets []ipc.Network, now time.Time) []notice {
	var out []notice
	seen := map[string]bool{}
	for _, net := range nets {
		seen[net.ID] = true
		cur := map[string]bool{}
		for _, m := range net.Members {
			cur[m.PublicKey] = true
		}
		old, known := n.members[net.ID]
		if n.membersReady && known && net.IsOwner {
			for _, m := range net.Members {
				if !m.IsSelf && !old[m.PublicKey] && !n.recentlyNotified(m.PublicKey, now) {
					n.notifiedAt[m.PublicKey] = now // no "is online" right after
					out = append(out, notice{Title: m.Nickname + " joined " + net.Name,
						Body: "They show up in your network when they are online."})
				}
			}
		}
		n.members[net.ID] = cur
	}
	for id := range n.members {
		if !seen[id] {
			delete(n.members, id)
		}
	}
	n.membersReady = true
	return out
}

// invites tells the user that a join they started has finished.
func (n *noticeState) invites(joins []ipc.PendingJoin) []notice {
	var out []notice
	for _, j := range joins {
		old := n.joins[j.NetworkID]
		if n.joinsReady && j.State == ipc.JoinJoined && old != "" && old != ipc.JoinJoined {
			name := j.NetworkName
			if name == "" {
				name = "the network"
			}
			out = append(out, notice{Title: "You joined " + name, Body: "Start your game when your friends are online."})
		}
		n.joins[j.NetworkID] = j.State
	}
	n.joinsReady = true
	return out
}

// updateFound announces a new release once.
func (n *noticeState) updateFound(u UpdateInfo) []notice {
	if !u.Available || u.Latest == "" || u.Latest == n.update {
		return nil
	}
	n.update = u.Latest
	return []notice{{Title: "Lanfolk " + u.Latest + " is available", Body: "Open Lanfolk to download it."}}
}
