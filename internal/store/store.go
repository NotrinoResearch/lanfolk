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

// Store guards the file.
type Store struct {
	dir  string
	mu   sync.Mutex
	data Data
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
// fails, nothing changes.
func (s *Store) Update(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := clone(s.data)
	if err := fn(&d); err != nil {
		return err
	}
	if err := s.save(d); err != nil {
		return err
	}
	s.data = d
	return nil
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
