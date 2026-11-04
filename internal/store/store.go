// Package store persists the client's local data model in one JSON file
// with atomic replacement. The plan's tables (identity, friends, networks,
// members, peer_cache, settings) are JSON arrays and objects; a database is
// not needed for at most a few hundred records (docs/DECISIONS.md D-019).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileName is the state file inside the data directory.
const FileName = "lanfolk.json"

// SchemaVersion is the current file format.
const SchemaVersion = 1

// MaxPeerCache bounds the last-known-good address cache.
const MaxPeerCache = 512

// Identity is the local user's key and nickname.
type Identity struct {
	PublicKey           string `json:"public_key"`
	EncryptedPrivateKey []byte `json:"encrypted_private_key"`
	Nickname            string `json:"nickname"`
}

// Friend is one friend entry. ConfirmedAt is zero while the friendship is
// one-sided (pending).
type Friend struct {
	PublicKey   string    `json:"public_key"`
	Nickname    string    `json:"nickname"`
	AddedAt     time.Time `json:"added_at"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
	ConfirmedAt time.Time `json:"confirmed_at,omitempty"`
	// PendingNetworks lists owned networks (hex IDs) this friend asked to
	// join with an invite code. The friend is added to each as soon as the
	// friendship is active (docs/DECISIONS.md D-025).
	PendingNetworks []string `json:"pending_networks,omitempty"`
}

// Invite is the invite code an owner created for one owned network.
type Invite struct {
	NetworkID string    `json:"network_id"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"created_at"`
}

// PendingJoin is an invite code the local user redeemed. State is "waiting"
// (the owner has not answered yet), "joining" (the owner accepted, the member
// list is on its way), "joined" or "expired".
type PendingJoin struct {
	NetworkID     string    `json:"network_id"`
	OwnerKey      string    `json:"owner_key"`
	Code          string    `json:"code"`
	State         string    `json:"state"`
	NetworkName   string    `json:"network_name,omitempty"`
	OwnerNickname string    `json:"owner_nickname,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Network is one private network; SignedList is the owner-signed member
// list (network.List encoding) and the source of truth.
type Network struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OwnerKey       string `json:"owner_key"`
	ListVersion    uint64 `json:"list_version"`
	OwnerSignature []byte `json:"owner_signature"`
	Active         bool   `json:"active"`
	SignedList     []byte `json:"signed_list"`
}

// Member mirrors the members of a network for convenience.
type Member struct {
	NetworkID string `json:"network_id"`
	PublicKey string `json:"public_key"`
	Nickname  string `json:"nickname"`
	VirtualIP string `json:"virtual_ip"`
}

// PeerCacheEntry is a last-known-good address of a peer.
type PeerCacheEntry struct {
	PublicKey     string    `json:"public_key"`
	Address       string    `json:"address"`
	PathType      string    `json:"path_type"`
	LastSuccessAt time.Time `json:"last_success_at"`
}

// Relay is a user-configured relay.
type Relay struct {
	Host    string `json:"host"`
	UDPPort int    `json:"udp_port"`
	TCPPort int    `json:"tcp_port"`
}

// Settings are user options. The relay fields of the plan's settings table
// live in CustomRelays; the default relay comes from the build.
type Settings struct {
	CustomRelays []Relay `json:"custom_relays"`
	CompatMode   bool    `json:"compat_mode"`
	ManualPort   int     `json:"manual_port"`
	RelayOnly    bool    `json:"relay_only"`
	PortMapping  bool    `json:"port_mapping"`
	DHTFallback  bool    `json:"dht_fallback"`
	ListenPort   int     `json:"listen_port"` // last automatic port, reused across restarts
}

// Data is the whole file.
type Data struct {
	SchemaVersion int              `json:"schema_version"`
	Identity      Identity         `json:"identity"`
	Friends       []Friend         `json:"friends"`
	Networks      []Network        `json:"networks"`
	Members       []Member         `json:"members"`
	PeerCache     []PeerCacheEntry `json:"peer_cache"`
	Settings      Settings         `json:"settings"`
	Invites       []Invite         `json:"invites,omitempty"`
	PendingJoins  []PendingJoin    `json:"pending_joins,omitempty"`
	// OwnerSID is the Windows account allowed to control the service through
	// the pipe (recorded at install, or by the first client).
	OwnerSID string `json:"owner_sid,omitempty"`
}

func defaults() Data {
	return Data{SchemaVersion: SchemaVersion, Settings: Settings{PortMapping: true, DHTFallback: true}}
}

// RememberPath records a working address of a peer, keeping the cache bounded.
func (d *Data) RememberPath(pub, addr, pathType string, at time.Time) {
	for i := range d.PeerCache {
		e := &d.PeerCache[i]
		if e.PublicKey == pub && e.Address == addr {
			e.PathType, e.LastSuccessAt = pathType, at
			return
		}
	}
	d.PeerCache = append(d.PeerCache, PeerCacheEntry{PublicKey: pub, Address: addr, PathType: pathType, LastSuccessAt: at})
	if len(d.PeerCache) > MaxPeerCache {
		sort.Slice(d.PeerCache, func(i, j int) bool { return d.PeerCache[i].LastSuccessAt.After(d.PeerCache[j].LastSuccessAt) })
		d.PeerCache = d.PeerCache[:MaxPeerCache]
	}
}

// LaterDelay is how long UpdateLater waits to batch changes into one write.
const LaterDelay = 5 * time.Second

// Store guards the file.
type Store struct {
	dir  string
	mu   sync.Mutex
	data Data

	// Deferred changes (UpdateLater). qmu is never held during disk I/O, so
	// queueing never blocks, even while a save is running.
	qmu   sync.Mutex
	queue []func(*Data)
	timer *time.Timer
}

// Open loads or creates the store in dir (created with mode 0700). A file
// that cannot be parsed is renamed aside and a fresh store is started, so a
// damaged file never prevents the service from running.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, data: defaults()}
	b, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, s.save(s.data)
	case err != nil:
		return nil, err
	}
	d := defaults()
	if err := json.Unmarshal(b, &d); err != nil || d.SchemaVersion == 0 {
		aside := fmt.Sprintf("%s.corrupt-%d", s.path(), time.Now().Unix())
		os.Rename(s.path(), aside)
		return s, s.save(s.data)
	}
	if d.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("state file was written by a newer Lanfolk (schema %d)", d.SchemaVersion)
	}
	d.SchemaVersion = SchemaVersion
	s.data = d
	return s, nil
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path() string { return filepath.Join(s.dir, FileName) }

func clone(d Data) Data {
	b, _ := json.Marshal(d)
	var c Data
	json.Unmarshal(b, &c)
	return c
}

// Data returns a deep copy of the current data.
func (s *Store) Data() Data {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.data)
}

// Update applies fn to a copy and saves it atomically. If fn or the save
// fails, nothing changes. Changes queued with UpdateLater are applied first,
// so they are saved too.
func (s *Store) Update(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.takeQueue() // under mu, so queued changes are applied in order
	d := clone(s.data)
	for _, f := range q {
		f(&d)
	}
	if err := fn(&d); err != nil {
		if len(q) > 0 {
			s.requeue(q)
		}
		return err
	}
	if err := s.save(d); err != nil {
		if len(q) > 0 {
			s.requeue(q)
		}
		return err
	}
	s.data = d
	return nil
}

// UpdateLater queues a change that may wait up to LaterDelay to be saved,
// together with other queued changes, in one write. It never blocks on disk
// I/O, so it is safe on the packet path. Use it for bookkeeping (last seen
// times, path cache); Data does not show the change until it is saved.
func (s *Store) UpdateLater(fn func(*Data)) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	s.queue = append(s.queue, fn)
	if s.timer == nil {
		s.timer = time.AfterFunc(LaterDelay, func() { s.Flush() })
	}
}

// Flush saves the changes queued with UpdateLater now.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.takeQueue()
	if len(q) == 0 {
		return nil
	}
	d := clone(s.data)
	for _, f := range q {
		f(&d)
	}
	if err := s.save(d); err != nil {
		s.requeue(q)
		return err
	}
	s.data = d
	return nil
}

func (s *Store) takeQueue() []func(*Data) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	q := s.queue
	s.queue = nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return q
}

// requeue puts changes back in front after a failed save; a later Update or
// Flush retries them.
func (s *Store) requeue(q []func(*Data)) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	s.queue = append(q, s.queue...)
	if s.timer == nil {
		s.timer = time.AfterFunc(LaterDelay, func() { s.Flush() })
	}
}

func (s *Store) save(d Data) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, FileName+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path())
}
