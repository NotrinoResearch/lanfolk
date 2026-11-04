package engine

import (
	"errors"
	"sort"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/invite"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// Invite parameters (docs/PROTOCOL.md, section 10.3; D-025).
const (
	// MaxJoinsPerInviteHour bounds how many new people one invite can bring
	// in per hour.
	MaxJoinsPerInviteHour = 30
	// joinClockSkew is how far a JoinRequest's timestamp may be from our clock.
	joinClockSkew = 10 * time.Minute
	// joinRetryInterval is how often a waiting joiner repeats its request.
	joinRetryInterval = 3 * time.Second
	// inviteFrameGap drops valid invite frames that follow each other faster.
	inviteFrameGap = 200 * time.Millisecond
	// maxPendingJoins bounds the codes a user can have in progress.
	maxPendingJoins = 16
	// joiningGrace is how long a joiner waits for the member list after the
	// owner accepted, even if the invite expires meanwhile.
	joiningGrace = 24 * time.Hour
	// keepFinishedJoin is how long a finished or expired join stays listed.
	keepFinishedJoin = 24 * time.Hour
)

// inviteSession is one invite code in use at the relays: the owner serves it,
// a joiner knocks on it. Immutable except lastFrame.
type inviteSession struct {
	sid       wire.SessionID
	key       [32]byte
	code      invite.Code
	owner     bool
	lastFrame atomic.Int64
}

// pendingJoin is a code the local user redeemed.
type pendingJoin struct {
	store.PendingJoin
	code     invite.Code
	lastSent time.Time
}

// sameCode compares the identity of two codes (the expiry is derived data).
func sameCode(a, b invite.Code) bool {
	return a.Token == b.Token && a.Network == b.Network && a.Owner == b.Owner
}

func newInviteSession(c invite.Code, owner bool) *inviteSession {
	return &inviteSession{sid: c.Session(), key: c.Key(), code: c, owner: owner}
}

// loadInvites restores invites and pending joins from the store. It runs in
// New, before anything else can touch the engine.
func (e *Engine) loadInvites(d store.Data) {
	for _, iv := range d.Invites {
		c, err := invite.Parse(iv.Code, time.Time{})
		nid, nerr := network.ParseID(iv.NetworkID)
		if err != nil || nerr != nil || c.Network != nid || c.Owner != e.key.Public() {
			continue
		}
		e.owned[nid] = c
	}
	for _, pj := range d.PendingJoins {
		c, err := invite.Parse(pj.Code, time.Time{})
		if err != nil {
			continue
		}
		e.pending[c.Network] = &pendingJoin{PendingJoin: pj, code: c}
	}
	for _, f := range d.Friends {
		if len(f.PendingNetworks) > 0 {
			e.hasPending.Store(true)
		}
	}
	e.syncInvitesLocked()
}

// saveInvitesLocked writes invites and pending joins to the store.
func (e *Engine) saveInvitesLocked() error {
	return e.store.Update(func(d *store.Data) error {
		d.Invites = d.Invites[:0]
		for nid, c := range e.owned {
			d.Invites = append(d.Invites, store.Invite{NetworkID: nid.String(), Code: c.String()})
		}
		d.PendingJoins = d.PendingJoins[:0]
		for _, pj := range e.pending {
			d.PendingJoins = append(d.PendingJoins, pj.PendingJoin)
		}
		sort.Slice(d.Invites, func(i, j int) bool { return d.Invites[i].NetworkID < d.Invites[j].NetworkID })
		sort.Slice(d.PendingJoins, func(i, j int) bool { return d.PendingJoins[i].NetworkID < d.PendingJoins[j].NetworkID })
		return nil
	})
}

// syncInvitesLocked makes the sessions registered at the relays match the
// unexpired owned invites and the joins still waiting for the owner.
func (e *Engine) syncInvitesLocked() {
	now := time.Now()
	want := make(map[wire.SessionID]*inviteSession)
	add := func(c invite.Code, owner bool) {
		is := newInviteSession(c, owner)
		if old := e.invSess[is.sid]; old != nil {
			is = old
		}
		want[is.sid] = is
	}
	for _, c := range e.owned {
		if !c.Expired(now) {
			add(c, true)
		}
	}
	for _, pj := range e.pending {
		if pj.State == ipc.JoinWaiting && !pj.code.Expired(now) {
			add(pj.code, false)
		}
	}
	for sid := range want {
		if e.invSess[sid] == nil {
			for _, r := range e.relays {
				r.client.Register(sid)
			}
		}
	}
	for sid := range e.invSess {
		if want[sid] == nil {
			for _, r := range e.relays {
				r.client.Unregister(sid)
			}
		}
	}
	e.invSess = want
	e.inviteIx.Store(&want)
}

// inviteSession finds an invite session by ID without locking.
func (e *Engine) inviteSession(sid wire.SessionID) *inviteSession {
	if m := e.inviteIx.Load(); m != nil {
		return (*m)[sid]
	}
	return nil
}

func (e *Engine) publishInvites() {
	if e.wantEvents() {
		e.emit(ipc.EventInvites, e.PendingJoins())
	}
}

// CreateInvite returns the invite code of an owned network. The current code
// is returned while it is valid; reset (or an expired code) creates a new one
// and so invalidates the old one.
func (e *Engine) CreateInvite(id network.ID, reset bool) (string, time.Time, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.ownedListLocked(id); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	if c, ok := e.owned[id]; ok && !reset && !c.Expired(now) {
		return c.String(), c.Expires, nil
	}
	c, err := invite.New(e.key.Public(), id, e.cfg.InviteTTL, now)
	if err != nil {
		return "", time.Time{}, err
	}
	e.owned[id] = c
	e.joinMu.Lock()
	delete(e.joinTimes, id)
	e.joinMu.Unlock()
	if err := e.saveInvitesLocked(); err != nil {
		delete(e.owned, id)
		return "", time.Time{}, err
	}
	e.syncInvitesLocked()
	return c.String(), c.Expires, nil
}

// RedeemInvite starts joining the network an invite code belongs to. The
// owner is added as a friend at once and the join completes when the owner is
// online (see docs/DECISIONS.md D-025).
func (e *Engine) RedeemInvite(code string) (ipc.RedeemResult, error) {
	c, err := invite.Parse(code, time.Now())
	switch {
	case errors.Is(err, invite.ErrExpired):
		return ipc.RedeemResult{}, ipc.Errorf(ipc.CodeInvalid, "this invite has expired; ask for a new one")
	case err != nil:
		return ipc.RedeemResult{}, ipc.Errorf(ipc.CodeInvalid, "that invite code is not valid; check that it was copied completely")
	}
	me := e.key.Public()
	if c.Owner == me {
		return ipc.RedeemResult{}, ipc.Errorf(ipc.CodeInvalid, "that is your own invite")
	}
	res := ipc.RedeemResult{NetworkID: c.Network.String(), OwnerPublicKey: c.Owner.Hex(), State: ipc.JoinWaiting}
	e.mu.Lock()
	if l := e.lists[c.Network]; l != nil && l.Has(me) {
		e.mu.Unlock()
		return res, ipc.Errorf(ipc.CodeInvalid, "you are already a member of this network")
	}
	if old := e.pending[c.Network]; old != nil && sameCode(old.code, c) && (old.State == ipc.JoinWaiting || old.State == ipc.JoinJoining) {
		res.State = old.State
		e.mu.Unlock()
		return res, nil
	}
	if e.pending[c.Network] == nil && len(e.pending) >= maxPendingJoins {
		e.mu.Unlock()
		return res, ipc.Errorf(ipc.CodeInvalid, "too many invites in progress; cancel one first")
	}
	e.mu.Unlock()

	// The owner becomes a friend now, so that the pair session is registered
	// and the friendship is confirmed as soon as the owner answers.
	err = e.store.Update(func(d *store.Data) error {
		for _, f := range d.Friends {
			if f.PublicKey == c.Owner.Hex() {
				return nil
			}
		}
		d.Friends = append(d.Friends, store.Friend{PublicKey: c.Owner.Hex(), AddedAt: time.Now().UTC(), ViaInvite: true})
		return nil
	})
	if err != nil {
		return res, err
	}
	now := time.Now()
	e.mu.Lock()
	e.reconcileLocked()
	p := e.peers[c.Owner]
	pj := &pendingJoin{code: c, PendingJoin: store.PendingJoin{NetworkID: res.NetworkID, OwnerKey: res.OwnerPublicKey,
		Code: c.String(), State: ipc.JoinWaiting, ExpiresAt: c.Expires, UpdatedAt: now.UTC()}}
	e.pending[c.Network] = pj
	err = e.saveInvitesLocked()
	e.syncInvitesLocked()
	e.mu.Unlock()
	if err != nil {
		return res, err
	}
	if p != nil {
		go e.sendHello(p)
	}
	e.publishFriends()
	e.publishInvites()
	if is := e.inviteSession(c.Session()); is != nil {
		go e.sendJoinRequest(is, true)
	}
	return res, nil
}

// CancelJoin forgets a redeemed code. An owner who became a friend only
// through this invite is removed again unless a network is shared.
func (e *Engine) CancelJoin(id network.ID) error {
	e.mu.Lock()
	if e.pending[id] == nil {
		e.mu.Unlock()
		return ipc.Errorf(ipc.CodeNotFound, "no invite in progress for this network")
	}
	delete(e.pending, id)
	err := e.saveInvitesLocked()
	e.syncInvitesLocked()
	e.mu.Unlock()
	e.publishInvites()
	e.pruneInviteFriends()
	return err
}

// dropInviteLocked forgets the invite of an owned network, so its code stops
// working at once. The owner gets a fresh code the next time one is asked
// for.
func (e *Engine) dropInviteLocked(id network.ID) {
	if _, ok := e.owned[id]; !ok {
		return
	}
	delete(e.owned, id)
	e.joinMu.Lock()
	delete(e.joinTimes, id)
	e.joinMu.Unlock()
	if err := e.saveInvitesLocked(); err != nil {
		e.log.Error("saving invites failed", "err", err)
	}
	e.syncInvitesLocked()
}

// pruneInviteFriends removes friendships that an invite created (ViaInvite)
// once they serve nothing: no shared network, no join in progress in either
// direction. Friends the user added by hand are never touched, and neither
// is a friend added in the last cfg.InviteFriendGrace (its pending join may
// not be recorded yet).
func (e *Engine) pruneInviteFriends() {
	keep := make(map[string]bool)
	e.mu.Lock()
	if e.ctx == nil || e.ctx.Err() != nil {
		e.mu.Unlock()
		return // not running (yet, or any more)
	}
	for _, l := range e.lists {
		for _, m := range l.Members {
			keep[m.PublicKey.Hex()] = true
		}
	}
	for _, pj := range e.pending {
		if pj.State == ipc.JoinWaiting || pj.State == ipc.JoinJoining {
			keep[pj.code.Owner.Hex()] = true
		}
	}
	e.mu.Unlock()
	cutoff := time.Now().Add(-e.cfg.InviteFriendGrace)
	removed := false
	err := e.store.Update(func(d *store.Data) error {
		out := d.Friends[:0]
		for _, f := range d.Friends {
			if f.ViaInvite && len(f.PendingNetworks) == 0 && !keep[f.PublicKey] && f.AddedAt.Before(cutoff) {
				removed = true
				continue
			}
			out = append(out, f)
		}
		d.Friends = out
		return nil
	})
	if err != nil || !removed {
		return
	}
	e.log.Info("removed friends that only an invite had added")
	e.mu.Lock()
	e.reconcileLocked()
	e.mu.Unlock()
	e.publishFriends()
}

// PendingJoins lists the invite codes the user redeemed and their progress.
func (e *Engine) PendingJoins() []ipc.PendingJoin {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ipc.PendingJoin, 0, len(e.pending))
	for nid, pj := range e.pending {
		j := ipc.PendingJoin{NetworkID: nid.String(), NetworkName: pj.NetworkName, OwnerPublicKey: pj.OwnerKey,
			OwnerNickname: pj.OwnerNickname, State: pj.State, Expires: pj.ExpiresAt}
		if j.OwnerNickname == "" {
			if p := e.peers[pj.code.Owner]; p != nil {
				p.mu.Lock()
				j.OwnerNickname = p.nick
				p.mu.Unlock()
			}
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NetworkID < out[j].NetworkID })
	return out
}

// sealInvite encrypts a message for an invite session.
func (e *Engine) sealInvite(is *inviteSession, m peermsg.Message) []byte {
	plain := peermsg.Marshal(peermsg.Header{Boot: e.boot, Counter: e.inviteSeq.Add(1)}, m)
	return wire.SealControl(make([]byte, 0, len(plain)+wire.ControlOverhead), &is.key, is.sid, plain)
}

// onInviteFrame handles a control frame that arrived in an invite session.
// Frames that do not open with the invite key are dropped without a trace.
func (e *Engine) onInviteFrame(r *relayConn, is *inviteSession, frame []byte) {
	plain, err := wire.OpenControl(&is.key, frame)
	if err != nil {
		return
	}
	_, msg, err := peermsg.Unmarshal(plain)
	if err != nil {
		return
	}
	now := nanotime()
	if prev := is.lastFrame.Load(); now-prev < int64(inviteFrameGap) || !is.lastFrame.CompareAndSwap(prev, now) {
		return
	}
	switch m := msg.(type) {
	case *peermsg.JoinRequest:
		if is.owner {
			go e.handleJoinRequest(is, r, m)
		}
	case *peermsg.JoinAccepted:
		if !is.owner {
			go e.handleJoinAccepted(is, m)
		}
	}
}

// allowJoin applies the per-invite rate limit to a new joiner.
func (e *Engine) allowJoin(nid network.ID, now time.Time) bool {
	e.joinMu.Lock()
	defer e.joinMu.Unlock()
	ts := e.joinTimes[nid][:0:0]
	for _, t := range e.joinTimes[nid] {
		if now.Sub(t) < time.Hour {
			ts = append(ts, t)
		}
	}
	if len(ts) >= MaxJoinsPerInviteHour {
		e.joinTimes[nid] = ts
		return false
	}
	e.joinTimes[nid] = append(ts, now)
	return true
}

var errJoinRateLimited = errors.New("too many joins for this invite")

// handleJoinRequest is the owner's side of a join. It validates the request,
// records the joiner as a friend with the network pending, answers with
// JoinAccepted through r (nil in tests) and reports whether it accepted.
func (e *Engine) handleJoinRequest(is *inviteSession, r *relayConn, m *peermsg.JoinRequest) bool {
	now := time.Now()
	me := e.key.Public()
	if !is.owner || is.code.Expired(now) || m.Network != is.code.Network || m.Joiner == me || m.Joiner.IsZero() {
		return false
	}
	if d := now.Sub(time.Unix(m.Timestamp, 0)); d > joinClockSkew || d < -joinClockSkew {
		return false
	}
	if !m.Joiner.Verify(invite.JoinRequestSigData(is.sid, m.Network, m.Joiner, m.Timestamp), m.Signature[:]) {
		return false
	}
	e.mu.Lock()
	cur, ok := e.owned[m.Network]
	l, err := e.ownedListLocked(m.Network)
	if !ok || !sameCode(cur, is.code) || err != nil {
		e.mu.Unlock() // the invite was reset or the network is gone
		return false
	}
	member := l.Has(m.Joiner)
	full := len(l.Members) >= network.MaxMembers
	e.mu.Unlock()
	if !member {
		if full {
			e.log.Info("join refused: network is full", "network", l.Name)
			return false
		}
		if err := e.addPendingFriend(m); err != nil {
			e.log.Info("join not accepted", "peer", m.Joiner.String(), "reason", err)
			return false
		}
		e.applyPendingMembers()
	}
	if r != nil {
		acc := &peermsg.JoinAccepted{Network: m.Network, NetworkName: l.Name}
		e.mu.Lock()
		acc.OwnerNickname = e.nick
		e.mu.Unlock()
		copy(acc.Signature[:], e.key.Sign(invite.JoinAcceptedSigData(is.sid, m.Network, m.Joiner)))
		r.client.SendData(is.sid, e.sealInvite(is, acc))
	}
	return true
}

// addPendingFriend adds the joiner as a friend (if it is not one yet) and
// notes that it wants into the network. A new joiner counts against the rate
// limit; a repeated request from the same joiner does not.
func (e *Engine) addPendingFriend(m *peermsg.JoinRequest) error {
	nid := m.Network.String()
	var limited bool
	err := e.store.Update(func(d *store.Data) error {
		for i := range d.Friends {
			f := &d.Friends[i]
			if f.PublicKey != m.Joiner.Hex() {
				continue
			}
			for _, p := range f.PendingNetworks {
				if p == nid {
					return nil
				}
			}
			if !e.allowJoin(m.Network, time.Now()) {
				limited = true
				return errJoinRateLimited
			}
			f.PendingNetworks = append(f.PendingNetworks, nid)
			e.hasPending.Store(true)
			return nil
		}
		if !e.allowJoin(m.Network, time.Now()) {
			limited = true
			return errJoinRateLimited
		}
		d.Friends = append(d.Friends, store.Friend{PublicKey: m.Joiner.Hex(), Nickname: cleanText(m.Nickname, network.MaxNicknameLen),
			AddedAt: time.Now().UTC(), PendingNetworks: []string{nid}, ViaInvite: true})
		e.hasPending.Store(true)
		return nil
	})
	if limited || err != nil {
		return err
	}
	e.mu.Lock()
	e.reconcileLocked()
	p := e.peers[m.Joiner]
	e.mu.Unlock()
	if p != nil {
		go e.sendHello(p)
	}
	e.publishFriends()
	return nil
}

// applyPendingMembers adds friends to the owned networks they asked to join
// as soon as the friendship is active.
func (e *Engine) applyPendingMembers() {
	if !e.hasPending.Load() {
		return
	}
	type job struct {
		pk  identity.PublicKey
		nid network.ID
	}
	var jobs []job
	for _, f := range e.store.Data().Friends {
		if f.ConfirmedAt.IsZero() {
			continue
		}
		pk, err := identity.ParsePublicKeyHex(f.PublicKey)
		if err != nil {
			continue
		}
		for _, s := range f.PendingNetworks {
			if nid, err := network.ParseID(s); err == nil {
				jobs = append(jobs, job{pk, nid})
			}
		}
	}
	done := make(map[job]bool)
	for _, j := range jobs {
		e.mu.Lock()
		l := e.lists[j.nid]
		gone := l == nil || l.Owner != e.key.Public() || l.Has(j.pk)
		e.mu.Unlock()
		if !gone {
			err := e.AddMember(j.nid, j.pk)
			if errors.Is(err, ErrNotFriend) {
				continue // the peer entry is not ready yet; the next tick retries
			}
			if err != nil {
				e.log.Warn("could not add a joiner to the network", "peer", j.pk.String(), "err", err)
			}
		}
		done[j] = true
	}
	e.store.Update(func(d *store.Data) error {
		remaining := false
		for i := range d.Friends {
			f := &d.Friends[i]
			pk, err := identity.ParsePublicKeyHex(f.PublicKey)
			if err != nil {
				continue
			}
			keep := f.PendingNetworks[:0]
			for _, s := range f.PendingNetworks {
				nid, err := network.ParseID(s)
				if err == nil && done[job{pk, nid}] {
					continue
				}
				keep = append(keep, s)
			}
			f.PendingNetworks = keep
			if len(keep) > 0 {
				remaining = true
			}
		}
		// Set under the store lock so that it cannot overtake a concurrent
		// addPendingFriend.
		e.hasPending.Store(remaining)
		return nil
	})
}

// handleJoinAccepted is the joiner's side: the owner said yes.
func (e *Engine) handleJoinAccepted(is *inviteSession, m *peermsg.JoinAccepted) {
	me := e.key.Public()
	if is.owner || m.Network != is.code.Network {
		return
	}
	if !is.code.Owner.Verify(invite.JoinAcceptedSigData(is.sid, m.Network, me), m.Signature[:]) {
		return
	}
	now := time.Now()
	e.mu.Lock()
	pj := e.pending[m.Network]
	if pj == nil || !sameCode(pj.code, is.code) || pj.State != ipc.JoinWaiting {
		e.mu.Unlock()
		return
	}
	pj.State = ipc.JoinJoining
	pj.NetworkName = cleanText(m.NetworkName, network.MaxNameLen)
	pj.OwnerNickname = cleanText(m.OwnerNickname, network.MaxNicknameLen)
	pj.ExpiresAt, pj.UpdatedAt = now.Add(joiningGrace), now.UTC()
	e.saveInvitesLocked()
	e.syncInvitesLocked() // leaves the invite session
	owner, nick := pj.code.Owner, pj.OwnerNickname
	e.mu.Unlock()
	e.log.Info("the network owner accepted the join", "owner", nick, "network", m.NetworkName)
	if nick != "" {
		e.store.Update(func(d *store.Data) error {
			for i := range d.Friends {
				if d.Friends[i].PublicKey == owner.Hex() && d.Friends[i].Nickname == "" {
					d.Friends[i].Nickname = nick
				}
			}
			return nil
		})
		e.publishFriends()
	}
	e.publishInvites()
}

// sendJoinRequest knocks on the invite session at every relay where the owner
// is present, and registers again where it is not (the relay may have said
// SessionFull while another joiner was inside).
func (e *Engine) sendJoinRequest(is *inviteSession, force bool) {
	now := time.Now()
	e.mu.Lock()
	pj := e.pending[is.code.Network]
	if pj == nil || !sameCode(pj.code, is.code) || pj.State != ipc.JoinWaiting || e.invSess[is.sid] != is ||
		(!force && now.Sub(pj.lastSent) < joinRetryInterval) {
		e.mu.Unlock()
		return
	}
	pj.lastSent = now
	relays := append([]*relayConn(nil), e.relays...)
	nick := e.nick
	e.mu.Unlock()

	req := &peermsg.JoinRequest{Network: is.code.Network, Joiner: e.key.Public(), Timestamp: now.Unix(), Nickname: nick}
	copy(req.Signature[:], e.key.Sign(invite.JoinRequestSigData(is.sid, req.Network, req.Joiner, req.Timestamp)))
	frame := e.sealInvite(is, req)
	for _, r := range relays {
		if r.client.PeerPresent(is.sid) {
			r.client.SendData(is.sid, frame)
		} else {
			r.client.Refresh(is.sid)
		}
	}
}

// tickInvites runs once a second: it retries waiting joins, ends joins that
// completed or ran out, drops old entries and finishes owner-side joins.
func (e *Engine) tickInvites(now time.Time) {
	me := e.key.Public()
	changed := false
	var due []*inviteSession
	e.mu.Lock()
	for nid, c := range e.owned {
		if l := e.lists[nid]; l == nil || l.Owner != me || c.Expired(now) {
			delete(e.owned, nid)
			changed = true
		}
	}
	for nid, pj := range e.pending {
		l := e.lists[nid]
		member := l != nil && l.Has(me)
		switch pj.State {
		case ipc.JoinWaiting, ipc.JoinJoining:
			if member {
				pj.State, pj.UpdatedAt = ipc.JoinJoined, now.UTC()
				changed = true
			} else if now.After(pj.ExpiresAt) {
				pj.State, pj.UpdatedAt = ipc.JoinExpired, now.UTC()
				changed = true
			} else if pj.State == ipc.JoinWaiting && now.Sub(pj.lastSent) >= joinRetryInterval {
				if is := e.invSess[pj.code.Session()]; is != nil {
					due = append(due, is)
				}
			}
		default:
			if now.Sub(pj.UpdatedAt) > keepFinishedJoin {
				delete(e.pending, nid)
				changed = true
			}
		}
	}
	if changed {
		e.saveInvitesLocked()
		e.syncInvitesLocked()
	}
	e.mu.Unlock()
	if changed {
		e.publishInvites()
	}
	for _, is := range due {
		e.sendJoinRequest(is, false)
	}
	e.applyPendingMembers()
}

// noteJoinedLocked marks a pending join as done when the member list arrived.
func (e *Engine) noteJoinedLocked(l *network.List) {
	pj := e.pending[l.ID]
	if pj == nil || (pj.State != ipc.JoinWaiting && pj.State != ipc.JoinJoining) || !l.Has(e.key.Public()) {
		return
	}
	pj.State, pj.UpdatedAt = ipc.JoinJoined, time.Now().UTC()
	if pj.NetworkName == "" {
		pj.NetworkName = l.Name
	}
	e.saveInvitesLocked()
	e.syncInvitesLocked()
	go e.publishInvites()
}
