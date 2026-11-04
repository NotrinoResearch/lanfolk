package engine

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/invite"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/peermsg"
	"github.com/NotrinoResearch/lanfolk/internal/store"
)

// Errors returned by membership operations.
var (
	ErrNotOwner       = errors.New("only the network owner can change membership")
	ErrUnknownNetwork = errors.New("unknown network")
	ErrNotFriend      = errors.New("only active friends can be added to a network")
)

// saveListsLocked writes the member lists and the active flag to the store.
func (e *Engine) saveListsLocked() error {
	return e.store.Update(func(d *store.Data) error {
		d.Networks = d.Networks[:0]
		d.Members = d.Members[:0]
		for _, l := range e.lists {
			d.Networks = append(d.Networks, store.Network{
				ID: l.ID.String(), Name: l.Name, OwnerKey: l.Owner.Hex(), ListVersion: l.Version,
				OwnerSignature: l.Signature, Active: e.active != nil && e.active.ID == l.ID, SignedList: l.Marshal(),
			})
			for _, m := range l.Members {
				d.Members = append(d.Members, store.Member{NetworkID: l.ID.String(), PublicKey: m.PublicKey.Hex(),
					Nickname: m.Nickname, VirtualIP: m.VirtualIP.String()})
			}
		}
		return nil
	})
}

// CreateNetwork creates a network owned by this user. It becomes active if
// no other network is.
func (e *Engine) CreateNetwork(name string) (*network.List, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, err := network.New(e.key, e.nick, name)
	if err != nil {
		return nil, err
	}
	e.lists[l.ID] = l
	if e.active == nil {
		e.activateLocked(l)
	}
	if err := e.saveListsLocked(); err != nil {
		return nil, err
	}
	go e.publishNetworks()
	return l.Clone(), nil
}

func (e *Engine) ownedListLocked(id network.ID) (*network.List, error) {
	l := e.lists[id]
	if l == nil {
		return nil, ErrUnknownNetwork
	}
	if l.Owner != e.key.Public() {
		return nil, ErrNotOwner
	}
	return l, nil
}

// AddMember adds an active friend to an owned network.
func (e *Engine) AddMember(id network.ID, pk identity.PublicKey) error {
	e.mu.Lock()
	l, err := e.ownedListLocked(id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	p := e.peers[pk]
	if p == nil || !p.isFriend || !p.confirmed {
		e.mu.Unlock()
		return ErrNotFriend
	}
	nl := l.Clone()
	if _, err := nl.AddMember(e.key, pk, p.displayName()); err != nil {
		e.mu.Unlock()
		return err
	}
	e.commitListLocked(nl)
	e.mu.Unlock()
	e.pushList(nl, nil)
	return nil
}

// RemoveMember removes a member from an owned network. The removed member
// is told too, so it drops the tunnel.
func (e *Engine) RemoveMember(id network.ID, pk identity.PublicKey) error {
	e.mu.Lock()
	l, err := e.ownedListLocked(id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	nl := l.Clone()
	if err := nl.RemoveMember(e.key, pk); err != nil {
		e.mu.Unlock()
		return err
	}
	removed := e.peers[pk]
	e.commitListLocked(nl)
	e.mu.Unlock()
	e.pushList(nl, removed)
	return nil
}

// RenameNetwork renames an owned network.
func (e *Engine) RenameNetwork(id network.ID, name string) error {
	return e.ownerChange(id, func(l *network.List) error { return l.Rename(e.key, name) })
}

// SetNetworkRelays sets the owner relays of an owned network.
func (e *Engine) SetNetworkRelays(id network.ID, relays []network.Relay) error {
	return e.ownerChange(id, func(l *network.List) error { return l.SetRelays(e.key, relays) })
}

func (e *Engine) ownerChange(id network.ID, fn func(*network.List) error) error {
	e.mu.Lock()
	l, err := e.ownedListLocked(id)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	nl := l.Clone()
	if err := fn(nl); err != nil {
		e.mu.Unlock()
		return err
	}
	e.commitListLocked(nl)
	e.mu.Unlock()
	e.pushList(nl, nil)
	return nil
}

// commitListLocked stores a newer list and applies it.
func (e *Engine) commitListLocked(l *network.List) {
	e.lists[l.ID] = l
	if e.active != nil && e.active.ID == l.ID {
		e.active = l
		e.applyRelaysLocked()
		e.reconcileLocked()
		e.updateTunnelAddressLocked()
	}
	if err := e.saveListsLocked(); err != nil {
		e.log.Error("saving member list failed", "err", err)
	}
	go e.publishNetworks()
}

// pushList sends a list to every member we have a session with, plus extra.
func (e *Engine) pushList(l *network.List, extra *peer) {
	e.mu.Lock()
	var targets []*peer
	for _, p := range e.peers {
		if l.Has(p.key) {
			targets = append(targets, p)
		}
	}
	e.mu.Unlock()
	if extra != nil {
		targets = append(targets, extra)
	}
	for _, p := range targets {
		e.sendList(p, l)
	}
}

func (e *Engine) sendList(p *peer, l *network.List) {
	var b [4]byte
	rand.Read(b[:])
	id := binary.BigEndian.Uint32(b[:])
	for _, f := range peermsg.Fragment(l.Marshal(), id) {
		e.sendMsg(p, f, nil)
	}
}

func (e *Engine) onMembershipFragment(p *peer, m *peermsg.MembershipUpdate) {
	p.mu.Lock()
	data := p.reasm.Add(m, time.Now())
	p.mu.Unlock()
	if data == nil {
		return
	}
	l, err := network.Unmarshal(data)
	if err != nil {
		e.log.Warn("rejected member list", "from", p.displayName(), "err", err)
		return
	}
	if err := e.acceptList(l, p); err != nil {
		e.log.Info("member list not applied", "from", p.displayName(), "network", l.Name, "reason", err)
	}
}

// acceptList applies a verified list received from a peer.
func (e *Engine) acceptList(l *network.List, from *peer) error {
	me := e.key.Public()
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.lists[l.ID]
	if cur == nil {
		// A network we do not know yet: accept it only from an owner who is
		// an active friend, and only if we are a member.
		if !l.Has(me) {
			return errors.New("not a member")
		}
		owner := e.peers[l.Owner]
		if owner == nil || !owner.isFriend || !owner.confirmed {
			return errors.New("network owner is not an active friend")
		}
	} else {
		newer, err := network.Supersedes(cur, l)
		if err != nil {
			return err
		}
		if !newer {
			return nil
		}
	}
	if !l.Has(me) {
		// We were removed.
		delete(e.lists, l.ID)
		if e.active != nil && e.active.ID == l.ID {
			e.deactivateLocked()
		}
		e.saveListsLocked()
		e.log.Info("removed from network", "network", l.Name)
		go e.publishNetworks()
		return nil
	}
	e.log.Info("member list updated", "network", l.Name, "version", l.Version, "from", from.displayName())
	e.commitListLocked(l)
	if e.active == nil {
		e.activateLocked(l)
		e.saveListsLocked()
	}
	e.noteJoinedLocked(l)
	// Gossip to members that may have an older list (owner offline case).
	go e.pushList(l, nil)
	return nil
}

// ActivateNetwork makes a network active; the zero ID deactivates.
func (e *Engine) ActivateNetwork(id network.ID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == (network.ID{}) {
		e.deactivateLocked()
		return e.saveListsLocked()
	}
	l := e.lists[id]
	if l == nil {
		return ErrUnknownNetwork
	}
	if e.active != nil && e.active.ID == id {
		return nil
	}
	e.activateLocked(l)
	return e.saveListsLocked()
}

func (e *Engine) activateLocked(l *network.List) {
	if e.active != nil {
		e.stopTunnelLocked()
	}
	e.active = l
	e.log.Info("network activated", "network", l.Name)
	e.applyRelaysLocked()
	e.reconcileLocked()
	if e.ctx != nil {
		e.startTunnelLocked()
	}
	for _, p := range e.peers {
		go e.sendHello(p)
	}
	go e.publishNetworks()
	go e.publishStatus()
}

func (e *Engine) deactivateLocked() {
	if e.active == nil {
		return
	}
	e.stopTunnelLocked()
	e.active = nil
	e.applyRelaysLocked()
	e.reconcileLocked()
	go e.publishNetworks()
	go e.publishStatus()
}

// LeaveNetwork deletes a network locally. An owner who leaves deletes the
// network for good: members keep their last list but it can never change.
func (e *Engine) LeaveNetwork(id network.ID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.lists[id]
	if l == nil {
		return ErrUnknownNetwork
	}
	if e.active != nil && e.active.ID == id {
		e.deactivateLocked()
	}
	delete(e.lists, id)
	if e.pending[id] != nil || e.owned[id] != (invite.Code{}) {
		delete(e.pending, id)
		delete(e.owned, id)
		e.saveInvitesLocked()
		e.syncInvitesLocked()
		go e.publishInvites()
	}
	go e.publishNetworks()
	return e.saveListsLocked()
}

// selfAddress returns our virtual IP in the active network.
func (e *Engine) selfAddressLocked() (network.Member, error) {
	if e.active == nil {
		return network.Member{}, errors.New("no active network")
	}
	m, ok := e.active.Member(e.key.Public())
	if !ok {
		return network.Member{}, fmt.Errorf("not a member of %s", e.active.Name)
	}
	return m, nil
}
