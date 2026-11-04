package main

import (
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

func netWith(id, name string, owner bool, keys ...string) ipc.Network {
	n := ipc.Network{ID: id, Name: name, Active: true, IsOwner: owner}
	n.Members = append(n.Members, ipc.Member{PublicKey: "me", Nickname: "Me", IsSelf: true})
	for _, k := range keys {
		n.Members = append(n.Members, ipc.Member{PublicKey: k, Nickname: "Nick-" + k})
	}
	return n
}

func peer(key string, online bool) ipc.Peer {
	return ipc.Peer{PublicKey: key, Nickname: "Nick-" + key, Online: online}
}

func titles(ns []notice) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Title)
	}
	return out
}

func TestOnlineNotices(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	nets := []ipc.Network{netWith("n1", "Friday games", false, "a", "b")}
	s := newNoticeState("")
	// The state found at start is never announced.
	if got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", false)}, t0); len(got) != 0 {
		t.Fatalf("start: %v", titles(got))
	}
	// b was offline when we started: coming online is news.
	got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", true)}, t0.Add(time.Second))
	if len(got) != 1 || got[0].Title != "Nick-b is online" || got[0].Body != "In Friday games" {
		t.Fatalf("b online: %+v", got)
	}
	// a blips for 30 s: not news.
	s.peers(nets, []ipc.Peer{peer("a", false), peer("b", true)}, t0.Add(10*time.Second))
	if got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", true)}, t0.Add(40*time.Second)); len(got) != 0 {
		t.Fatalf("blip: %v", titles(got))
	}
	// b leaves for 5 minutes, but was announced 6 minutes ago: not again yet.
	s.peers(nets, []ipc.Peer{peer("a", true), peer("b", false)}, t0.Add(time.Minute))
	if got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", true)}, t0.Add(6*time.Minute)); len(got) != 0 {
		t.Fatalf("repeat: %v", titles(got))
	}
	// a leaves for 20 minutes: announced.
	s.peers(nets, []ipc.Peer{peer("a", false), peer("b", true)}, t0.Add(10*time.Minute))
	if got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", true)}, t0.Add(30*time.Minute)); len(got) != 1 {
		t.Fatalf("return: %v", titles(got))
	}
	// Switching networks starts over silently.
	other := []ipc.Network{netWith("n2", "Weekend", false, "c")}
	if got := s.peers(other, []ipc.Peer{peer("c", true)}, t0.Add(31*time.Minute)); len(got) != 0 {
		t.Fatalf("switch: %v", titles(got))
	}
	// No active network: nothing.
	if got := s.peers(nil, nil, t0.Add(32*time.Minute)); len(got) != 0 {
		t.Fatal("no network")
	}
}

func TestJoinedNotices(t *testing.T) {
	t0 := time.Now()
	s := newNoticeState("")
	if got := s.networks([]ipc.Network{netWith("n1", "Friday", true, "a")}, t0); len(got) != 0 {
		t.Fatalf("start: %v", titles(got))
	}
	got := s.networks([]ipc.Network{netWith("n1", "Friday", true, "a", "b")}, t0)
	if len(got) != 1 || got[0].Title != "Nick-b joined Friday" {
		t.Fatalf("owner: %+v", got)
	}
	// No "is online" right after "joined".
	nets := []ipc.Network{netWith("n1", "Friday", true, "a", "b")}
	s.peers(nets, []ipc.Peer{peer("a", true)}, t0)
	if got := s.peers(nets, []ipc.Peer{peer("a", true), peer("b", true)}, t0.Add(5*time.Second)); len(got) != 0 {
		t.Fatalf("double notice: %v", titles(got))
	}
	// Members of networks we do not own are not announced.
	if got := s.networks([]ipc.Network{netWith("n1", "Friday", true, "a", "b"), netWith("n2", "Theirs", false, "x")}, t0); len(got) != 0 {
		t.Fatal("new network announced")
	}
	if got := s.networks([]ipc.Network{netWith("n1", "Friday", true, "a", "b"), netWith("n2", "Theirs", false, "x", "y")}, t0); len(got) != 0 {
		t.Fatal("member of someone else's network announced")
	}
}

func TestInviteAndUpdateNotices(t *testing.T) {
	s := newNoticeState("0.3.0")
	s.invites([]ipc.PendingJoin{{NetworkID: "n1", State: ipc.JoinJoined}, {NetworkID: "n2", State: ipc.JoinWaiting}})
	got := s.invites([]ipc.PendingJoin{{NetworkID: "n1", State: ipc.JoinJoined}, {NetworkID: "n2", NetworkName: "Friday", State: ipc.JoinJoined}})
	if len(got) != 1 || got[0].Title != "You joined Friday" {
		t.Fatalf("join: %+v", got)
	}
	if got := s.updateFound(UpdateInfo{Available: true, Latest: "0.3.0"}); len(got) != 0 {
		t.Fatal("announced again")
	}
	if got := s.updateFound(UpdateInfo{Available: true, Latest: "0.3.1"}); len(got) != 1 || got[0].Title != "Lanfolk 0.3.1 is available" {
		t.Fatalf("update: %+v", got)
	}
	if got := s.updateFound(UpdateInfo{Available: true, Latest: "0.3.1"}); len(got) != 0 {
		t.Fatal("same version twice")
	}
	if got := s.updateFound(UpdateInfo{Available: false, Latest: "0.3.2"}); len(got) != 0 {
		t.Fatal("not available")
	}
}
