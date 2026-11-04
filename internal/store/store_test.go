package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateLoadAndUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Data().Settings.PortMapping || !s.Data().Settings.DHTFallback {
		t.Fatal("defaults not applied")
	}
	err = s.Update(func(d *Data) error {
		d.Identity.Nickname = "Ahmet"
		d.Friends = append(d.Friends, Friend{PublicKey: "aa", Nickname: "Mehmet", AddedAt: time.Unix(10, 0).UTC()})
		d.Settings.RelayOnly = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := s2.Data()
	if d.Identity.Nickname != "Ahmet" || len(d.Friends) != 1 || !d.Settings.RelayOnly {
		t.Fatalf("not persisted: %+v", d)
	}
	// Data returns a copy: mutating it does not change the store.
	d.Friends[0].Nickname = "changed"
	if s2.Data().Friends[0].Nickname != "Mehmet" {
		t.Fatal("Data is not a deep copy")
	}
}

func TestUpdateErrorDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Update(func(d *Data) error { d.Identity.Nickname = "x"; return os.ErrInvalid })
	if s.Data().Identity.Nickname == "x" {
		t.Fatal("failed update applied in memory")
	}
	s2, _ := Open(dir)
	if s2.Data().Identity.Nickname == "x" {
		t.Fatal("failed update persisted")
	}
}

func TestCorruptFileIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Data().SchemaVersion != SchemaVersion {
		t.Fatal("store not reinitialised")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, FileName+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatal("corrupt file was not preserved for inspection")
	}
}

func TestPeerCacheBounded(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < 3*MaxPeerCache; i++ {
		s.Update(func(d *Data) error {
			d.RememberPath("k", "198.51.100.1:"+string(rune('0'+i%10)), "direct", time.Unix(int64(i), 0))
			return nil
		})
	}
	if n := len(s.Data().PeerCache); n > MaxPeerCache {
		t.Fatalf("peer cache has %d entries", n)
	}
}
