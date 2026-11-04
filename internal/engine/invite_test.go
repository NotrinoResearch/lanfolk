package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/invite"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
)

// joinState returns the state of n's pending join for the network, or "".
func joinState(n *node, nid string) string {
	for _, p := range n.e.PendingJoins() {
		if p.NetworkID == nid {
			return p.State
		}
	}
	return ""
}

func isActiveMember(n *node, nid string) bool {
	st := n.e.Status()
	return st.ActiveNetworkID == nid && st.AdapterUp
}

func mustInvite(t *testing.T, owner *node, nid string, reset bool) (string, time.Time) {
	t.Helper()
	id, err := network.ParseID(nid)
	if err != nil {
		t.Fatal(err)
	}
	code, exp, err := owner.e.CreateInvite(id, reset)
	if err != nil {
		t.Fatal(err)
	}
	return code, exp
}

func TestInviteJoinEndToEnd(t *testing.T) {
	r := startTestRelay(t)
	a, b, c := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "can", r)
	l, err := a.e.CreateNetwork("Friday games")
	if err != nil {
		t.Fatal(err)
	}
	nid := l.ID.String()

	code, exp := mustInvite(t, a, nid, false)
	if !strings.HasPrefix(code, invite.Prefix) {
		t.Fatalf("code %q", code)
	}
	if d := time.Until(exp); d < 6*24*time.Hour || d > 7*24*time.Hour+time.Minute {
		t.Fatalf("invite valid for %v", d)
	}
	// The same valid code is returned until it is reset.
	if again, _ := mustInvite(t, a, nid, false); again != code {
		t.Fatal("a second create returned a different code")
	}

	res, err := b.e.RedeemInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	if res.NetworkID != nid || res.OwnerPublicKey != a.e.PublicKey().Hex() || res.State != ipc.JoinWaiting {
		t.Fatalf("redeem result %+v", res)
	}
	waitFor(t, 20*time.Second, "mehmet to join and activate the network", func() bool {
		return isActiveMember(b, nid) && len(b.e.Peers()) == 1 && joinState(b, nid) == ipc.JoinJoined
	})
	if friendState(a, b) != "active" || friendState(b, a) != "active" {
		t.Fatalf("friendship: %q %q", friendState(a, b), friendState(b, a))
	}
	for _, p := range b.e.PendingJoins() {
		if p.NetworkName != "Friday games" || p.OwnerNickname != "ahmet" {
			t.Fatalf("pending join details: %+v", p)
		}
	}
	sendUntil(t, a, b, 9000, []byte("owner to joiner"))
	sendUntil(t, b, a, 9000, []byte("joiner to owner"))

	// A second friend uses the same reusable code.
	if _, err := c.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "can to join the network", func() bool {
		return isActiveMember(c, nid) && len(c.e.Peers()) == 2
	})
	sendUntil(t, c, a, 9001, []byte("can to owner"))
	sendUntil(t, c, b, 9002, []byte("can to mehmet"))
	waitFor(t, 10*time.Second, "everyone sees three members", func() bool {
		for _, n := range []*node{a, b, c} {
			ns := n.e.Networks()
			if len(ns) != 1 || len(ns[0].Members) != 3 {
				return false
			}
		}
		return true
	})
	// Redeeming a code for a network you are in is refused.
	if _, err := c.e.RedeemInvite(code); err == nil {
		t.Fatal("redeeming for a network the user already belongs to succeeded")
	}
}

func TestInviteExpiredCodeIsRejected(t *testing.T) {
	r := startTestRelay(t)
	a := newNodeIn(t, "ahmet", r, t.TempDir(), func(c *Config) { c.InviteTTL = 2 * time.Second })
	b := newNode(t, "mehmet", r)
	l, _ := a.e.CreateNetwork("Short lived")
	code, _ := mustInvite(t, a, l.ID.String(), false)
	time.Sleep(3 * time.Second)
	if _, err := b.e.RedeemInvite(code); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired code redeemed: %v", err)
	}
	// Asking again after expiry hands out a fresh code.
	fresh, _ := mustInvite(t, a, l.ID.String(), false)
	if fresh == code {
		t.Fatal("expired invite was returned again")
	}
	// Garbage and own codes are refused with a message the UI can show.
	if _, err := b.e.RedeemInvite(code[:30]); err == nil {
		t.Fatal("truncated code accepted")
	}
	if _, err := a.e.RedeemInvite(fresh); err == nil {
		t.Fatal("own invite accepted")
	}
	// The owner side also stops serving an invite that ran out while the
	// joiner was already waiting.
	b2 := newNode(t, "can", r)
	if _, err := b2.e.RedeemInvite(fresh); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "joiner to be told the invite is gone or to join", func() bool {
		s := joinState(b2, l.ID.String())
		return s == ipc.JoinJoined || s == ipc.JoinExpired
	})
}

func TestInviteResetInvalidatesOldCode(t *testing.T) {
	r := startTestRelay(t)
	a, b, c := newNode(t, "ahmet", r), newNode(t, "mehmet", r), newNode(t, "can", r)
	l, _ := a.e.CreateNetwork("Reset me")
	nid := l.ID.String()
	old, _ := mustInvite(t, a, nid, false)
	fresh, _ := mustInvite(t, a, nid, true)
	if fresh == old {
		t.Fatal("reset returned the same code")
	}
	if _, err := b.e.RedeemInvite(old); err != nil {
		t.Fatal(err)
	}
	if _, err := c.e.RedeemInvite(fresh); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "can to join with the new code", func() bool { return isActiveMember(c, nid) })
	time.Sleep(3 * time.Second)
	if s := joinState(b, nid); s != ipc.JoinWaiting {
		t.Fatalf("old code state %q, want waiting", s)
	}
	if ns := b.e.Networks(); len(ns) != 0 {
		t.Fatalf("old code joined: %+v", ns)
	}
	if len(a.e.Networks()[0].Members) != 2 {
		t.Fatalf("members: %+v", a.e.Networks()[0].Members)
	}
	// Only the owner can create or reset.
	id, _ := network.ParseID(nid)
	if _, _, err := c.e.CreateInvite(id, false); err == nil {
		t.Fatal("a member created an invite")
	}
	if _, _, err := a.e.CreateInvite(network.ID{9}, false); err == nil {
		t.Fatal("invite for an unknown network")
	}
}

func TestInviteOwnerOfflineAtRedeem(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	l, _ := a.e.CreateNetwork("Late owner")
	nid := l.ID.String()
	code, _ := mustInvite(t, a, nid, false)
	a.stop()

	if _, err := b.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if s := joinState(b, nid); s != ipc.JoinWaiting {
		t.Fatalf("state %q while the owner is offline", s)
	}
	// The pending join survives a restart of the joiner's service.
	b.stop()
	b = newNodeIn(t, "mehmet", r, b.dir, nil)
	if s := joinState(b, nid); s != ipc.JoinWaiting {
		t.Fatalf("state %q after restart", s)
	}
	a = newNodeIn(t, "ahmet", r, a.dir, nil)
	waitFor(t, 25*time.Second, "the join to complete once the owner is back", func() bool {
		return isActiveMember(b, nid) && joinState(b, nid) == ipc.JoinJoined
	})
	sendUntil(t, b, a, 9100, []byte("after the owner came back"))
}

func TestInviteSurvivesOwnerRestartMidJoin(t *testing.T) {
	// The owner accepts, then restarts before the friendship is confirmed.
	// The pending network is stored with the friend, so the join still ends.
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	l, _ := a.e.CreateNetwork("Restart")
	nid := l.ID.String()
	code, _ := mustInvite(t, a, nid, false)
	if _, err := b.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "owner to store the pending friend", func() bool {
		return len(a.e.store.Data().Friends) == 1
	})
	a.stop()
	a = newNodeIn(t, "ahmet", r, a.dir, nil)
	waitFor(t, 25*time.Second, "join to complete after the owner restarted", func() bool { return isActiveMember(b, nid) })
}

// forgedJoin builds a JoinRequest for the invite as a holder of the code
// would, with fields the caller can break.
func forgedJoin(code invite.Code, k *identity.PrivateKey, ts time.Time) *peermsg.JoinRequest {
	m := &peermsg.JoinRequest{Network: code.Network, Joiner: k.Public(), Timestamp: ts.Unix(), Nickname: "Intruder"}
	copy(m.Signature[:], k.Sign(invite.JoinRequestSigData(code.Session(), code.Network, k.Public(), ts.Unix())))
	return m
}

func ownerSession(t *testing.T, o *node, nid string) (*inviteSession, invite.Code) {
	t.Helper()
	id, _ := network.ParseID(nid)
	o.e.mu.Lock()
	defer o.e.mu.Unlock()
	for _, is := range o.e.invSess {
		if is.owner && is.code.Network == id {
			return is, is.code
		}
	}
	t.Fatal("no owner invite session")
	return nil, invite.Code{}
}

func TestInviteForgedRequestsAreIgnored(t *testing.T) {
	r := startTestRelay(t)
	a := newNode(t, "ahmet", r)
	l, _ := a.e.CreateNetwork("Guarded")
	nid := l.ID.String()
	mustInvite(t, a, nid, false)
	is, code := ownerSession(t, a, nid)
	now := time.Now()
	k, _ := identity.Generate()
	other, _ := identity.Generate()

	bad := map[string]*peermsg.JoinRequest{}
	wrongNet := forgedJoin(code, k, now)
	wrongNet.Network[0] ^= 1
	bad["wrong network"] = wrongNet
	badSig := forgedJoin(code, k, now)
	badSig.Signature[5] ^= 0xFF
	bad["flipped signature"] = badSig
	swapped := forgedJoin(code, k, now) // signed by k, claims to be "other"
	swapped.Joiner = other.Public()
	bad["someone else's key"] = swapped
	bad["stale timestamp"] = forgedJoin(code, k, now.Add(-11*time.Minute))
	bad["future timestamp"] = forgedJoin(code, k, now.Add(11*time.Minute))
	self := forgedJoin(code, a.e.key, now)
	bad["owner's own key"] = self
	// A signature made for another invite session does not carry over.
	otherCode := code
	otherCode.Token[0] ^= 1
	bad["signature for another invite"] = forgedJoin(otherCode, k, now)
	for name, m := range bad {
		if a.e.handleJoinRequest(is, nil, m) {
			t.Errorf("%s: request was accepted", name)
		}
	}
	if n := len(a.e.store.Data().Friends); n != 0 {
		t.Fatalf("forged requests added %d friends", n)
	}

	// A frame sealed with a key other than the invite key is dropped before
	// it is decoded.
	wrongKey := code
	wrongKey.Token[1] ^= 1
	frame := a.e.sealInvite(&inviteSession{sid: is.sid, key: wrongKey.Key()}, forgedJoin(code, k, now))
	a.e.onInviteFrame(nil, is, frame)
	if n := len(a.e.store.Data().Friends); n != 0 {
		t.Fatalf("frame with the wrong key added %d friends", n)
	}

	// The same request, made correctly, is accepted, and is idempotent.
	good := forgedJoin(code, k, now)
	if !a.e.handleJoinRequest(is, nil, good) || !a.e.handleJoinRequest(is, nil, good) {
		t.Fatal("valid request was rejected")
	}
	fs := a.e.store.Data().Friends
	if len(fs) != 1 || fs[0].PublicKey != k.Public().Hex() || fs[0].Nickname != "Intruder" || len(fs[0].PendingNetworks) != 1 {
		t.Fatalf("friends after a valid request: %+v", fs)
	}
	// The friend is not a member before it has confirmed the friendship.
	if got := a.e.Networks()[0]; len(got.Members) != 1 {
		t.Fatalf("unconfirmed joiner became a member: %+v", got.Members)
	}
}

func TestInviteJoinRateLimit(t *testing.T) {
	r := startTestRelay(t)
	a := newNode(t, "ahmet", r)
	l, _ := a.e.CreateNetwork("Busy")
	nid := l.ID.String()
	mustInvite(t, a, nid, false)
	is, code := ownerSession(t, a, nid)
	accepted := 0
	for i := 0; i < MaxJoinsPerInviteHour+5; i++ {
		k, _ := identity.Generate()
		if a.e.handleJoinRequest(is, nil, forgedJoin(code, k, time.Now())) {
			accepted++
		}
	}
	if accepted != MaxJoinsPerInviteHour {
		t.Fatalf("accepted %d joins in an hour, want %d", accepted, MaxJoinsPerInviteHour)
	}
	// A reset gives a fresh allowance.
	mustInvite(t, a, nid, true)
	is, code = ownerSession(t, a, nid)
	k, _ := identity.Generate()
	if !a.e.handleJoinRequest(is, nil, forgedJoin(code, k, time.Now())) {
		t.Fatal("reset did not clear the rate limit")
	}
}

func TestInviteRepeatedRequestFromMemberIsAcceptedAgain(t *testing.T) {
	r := startTestRelay(t)
	a, b := newNode(t, "ahmet", r), newNode(t, "mehmet", r)
	l, _ := a.e.CreateNetwork("Idempotent")
	nid := l.ID.String()
	code, _ := mustInvite(t, a, nid, false)
	if _, err := b.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "join", func() bool { return isActiveMember(b, nid) })
	is, c := ownerSession(t, a, nid)
	if !a.e.handleJoinRequest(is, nil, forgedJoin(c, b.e.key, time.Now())) {
		t.Fatal("request from an existing member refused")
	}
	if n := len(a.e.Networks()[0].Members); n != 2 {
		t.Fatalf("%d members", n)
	}
}

func TestInviteIPC(t *testing.T) {
	r := startTestRelay(t)
	var mu sync.Mutex
	var events []string
	a := newNode(t, "ahmet", r)
	b := newNodeIn(t, "mehmet", r, t.TempDir(), func(c *Config) {
		c.OnEvent = func(ev string, data any) {
			if ev == ipc.EventInvites {
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			}
		}
	})
	ctx := context.Background()
	call := func(n *node, method string, params any, out any) error {
		raw, _ := json.Marshal(params)
		res, err := n.e.Handle(ctx, method, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(res)
		return json.Unmarshal(b, out)
	}
	var net ipc.Network
	if err := call(a, ipc.MethodCreateNetwork, ipc.CreateNetworkParams{Name: "IPC"}, &net); err != nil {
		t.Fatal(err)
	}
	var inv ipc.InviteResult
	if err := call(a, ipc.MethodCreateInvite, ipc.InviteParams{NetworkID: net.ID}, &inv); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inv.Code, "lanfolk-i1") || inv.Expires.IsZero() {
		t.Fatalf("invite %+v", inv)
	}
	var red ipc.RedeemResult
	if err := call(b, ipc.MethodRedeemInvite, ipc.CodeParams{Code: inv.Code}, &red); err != nil {
		t.Fatal(err)
	}
	if red.NetworkID != net.ID || red.OwnerPublicKey != a.e.PublicKey().Hex() || red.State != "waiting" {
		t.Fatalf("redeem %+v", red)
	}
	var list []ipc.PendingJoin
	if err := call(b, ipc.MethodListInvites, nil, &list); err != nil || len(list) != 1 || list[0].NetworkID != net.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	waitFor(t, 20*time.Second, "joined", func() bool {
		if err := call(b, ipc.MethodListInvites, nil, &list); err != nil || len(list) != 1 {
			return false
		}
		return list[0].State == "joined"
	})
	if list[0].NetworkName != "IPC" || list[0].OwnerNickname != "ahmet" || list[0].OwnerPublicKey != a.e.PublicKey().Hex() {
		t.Fatalf("joined entry %+v", list[0])
	}
	mu.Lock()
	n := len(events)
	mu.Unlock()
	if n < 2 {
		t.Fatalf("got %d invites events, want one for the redeem and one per state change", n)
	}
	// Bad parameters and cancel.
	if err := call(b, ipc.MethodRedeemInvite, ipc.CodeParams{Code: "nonsense"}, &red); err == nil {
		t.Fatal("garbage code accepted")
	}
	if err := call(a, ipc.MethodCreateInvite, ipc.InviteParams{NetworkID: "zz"}, &inv); err == nil {
		t.Fatal("bad network id accepted")
	}
	c := newNode(t, "can", r)
	var inv2 ipc.InviteResult
	call(a, ipc.MethodCreateInvite, ipc.InviteParams{NetworkID: net.ID, Reset: true}, &inv2)
	if inv2.Code == inv.Code {
		t.Fatal("reset over IPC kept the code")
	}
	if err := call(c, ipc.MethodRedeemInvite, ipc.CodeParams{Code: inv.Code}, &red); err != nil {
		t.Fatal(err)
	}
	if err := call(c, ipc.MethodCancelInvite, ipc.NetworkParams{NetworkID: net.ID}, new(bool)); err != nil {
		t.Fatal(err)
	}
	if err := call(c, ipc.MethodListInvites, nil, &list); err != nil || len(list) != 0 {
		t.Fatalf("after cancel: %+v %v", list, err)
	}
	if err := call(c, ipc.MethodCancelInvite, ipc.NetworkParams{NetworkID: net.ID}, new(bool)); err == nil {
		t.Fatal("cancelling nothing succeeded")
	}
}

func TestInviteSimultaneousJoiners(t *testing.T) {
	// Only one joiner fits in the invite session next to the owner. The
	// others are told SessionFull and must get in once it is free.
	r := startTestRelay(t)
	a := newNode(t, "ahmet", r)
	l, _ := a.e.CreateNetwork("Crowd")
	nid := l.ID.String()
	code, _ := mustInvite(t, a, nid, false)
	joiners := []*node{newNode(t, "b1", r), newNode(t, "b2", r), newNode(t, "b3", r)}
	for _, j := range joiners {
		if _, err := j.e.RedeemInvite(code); err != nil {
			t.Fatal(err)
		}
	}
	for _, j := range joiners {
		j := j
		waitFor(t, 30*time.Second, j.name+" to join", func() bool { return joinState(j, nid) == ipc.JoinJoined })
	}
	if n := len(a.e.Networks()[0].Members); n != 4 {
		t.Fatalf("%d members, want 4", n)
	}
}

// fastPrune makes invite-only friendships end quickly in tests.
func fastPrune(c *Config) {
	c.InviteFriendGrace = time.Millisecond
	c.RemovalPruneDelay = 300 * time.Millisecond
}

// joinWithInvite lets joiner join owner's network with a fresh code.
func joinWithInvite(t *testing.T, owner, joiner *node, nid string) string {
	t.Helper()
	code, _ := mustInvite(t, owner, nid, false)
	if _, err := joiner.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, joiner.name+" to join", func() bool { return isActiveMember(joiner, nid) })
	return code
}

// Removing a member stops the invite code it used and ends the friendship
// that only the invite created, on both sides.
func TestRemoveMemberResetsInviteAndEndsInviteFriendship(t *testing.T) {
	r := startTestRelay(t)
	a := newNodeIn(t, "ahmet", r, t.TempDir(), fastPrune)
	b := newNodeIn(t, "mehmet", r, t.TempDir(), fastPrune)
	c := newNodeIn(t, "can", r, t.TempDir(), fastPrune)
	l, err := a.e.CreateNetwork("Friday games")
	if err != nil {
		t.Fatal(err)
	}
	nid := l.ID.String()
	code := joinWithInvite(t, a, b, nid)

	if err := a.e.RemoveMember(l.ID, b.e.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := mustInvite(t, a, nid, false); fresh == code {
		t.Fatal("the invite code still works after removing a member")
	}
	waitFor(t, 15*time.Second, "the invite-only friendship to end on both sides", func() bool {
		return friendState(a, b) == "" && friendState(b, a) == "" && len(b.e.Networks()) == 0
	})
	// The old code no longer opens the network.
	if _, err := c.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if joinState(c, nid) != ipc.JoinWaiting || len(c.e.Networks()) != 0 {
		t.Fatalf("old code: state %q, networks %d", joinState(c, nid), len(c.e.Networks()))
	}
	// The waiting joiner keeps the owner as a friend while it waits.
	if friendState(c, a) == "" {
		t.Fatal("the owner friend of a waiting join was pruned")
	}
}

// Friends the user added by hand, or kept by adding them again, stay.
func TestRemoveMemberKeepsChosenFriends(t *testing.T) {
	r := startTestRelay(t)
	a := newNodeIn(t, "ahmet", r, t.TempDir(), fastPrune)
	b := newNodeIn(t, "mehmet", r, t.TempDir(), fastPrune)
	c := newNodeIn(t, "can", r, t.TempDir(), fastPrune)
	befriend(t, a, b)
	nid := createNetwork(t, a, b)
	id := mustNetID(t, nid)
	joinWithInvite(t, a, c, nid)
	// can decides to keep ahmet as a friend: adding an invite friend again
	// makes the friendship permanent instead of failing.
	if _, err := c.e.AddFriend(a.code(), "ahmet"); err != nil {
		t.Fatal(err)
	}
	if err := a.e.RemoveMember(id, b.e.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := a.e.RemoveMember(id, c.e.PublicKey()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "both to drop the network", func() bool {
		return len(b.e.Networks()) == 0 && len(c.e.Networks()) == 0
	})
	time.Sleep(time.Second) // past the removal delay
	if friendState(a, b) != "active" || friendState(b, a) != "active" {
		t.Fatalf("hand-added friendship changed: %q %q", friendState(a, b), friendState(b, a))
	}
	if friendState(c, a) == "" {
		t.Fatal("a friend kept on purpose was pruned")
	}
	if friendState(a, c) != "" {
		t.Fatal("the owner kept an invite-only friend")
	}
}

// Cancelling a join drops the owner friend that the invite added.
func TestCancelJoinDropsInviteOwnerFriend(t *testing.T) {
	r := startTestRelay(t)
	a := newNodeIn(t, "ahmet", r, t.TempDir(), fastPrune)
	b := newNodeIn(t, "mehmet", r, t.TempDir(), fastPrune)
	l, _ := a.e.CreateNetwork("Later")
	code, _ := mustInvite(t, a, l.ID.String(), false)
	a.stop() // the owner is offline, so the join waits
	if _, err := b.e.RedeemInvite(code); err != nil {
		t.Fatal(err)
	}
	if friendState(b, a) == "" {
		t.Fatal("redeeming did not add the owner")
	}
	if err := b.e.CancelJoin(l.ID); err != nil {
		t.Fatal(err)
	}
	if friendState(b, a) != "" {
		t.Fatalf("owner still a friend after cancel: %q", friendState(b, a))
	}
}
