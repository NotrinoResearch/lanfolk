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

func TestUpdateLaterBatchesAndFlushes(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Update(func(d *Data) error {
		d.Friends = append(d.Friends, Friend{PublicKey: "aa"})
		return nil
	})
	fi, _ := os.Stat(filepath.Join(dir, FileName))
	before := fi.ModTime()
	for i := 1; i <= 50; i++ {
		at := time.Unix(int64(i), 0).UTC()
		s.UpdateLater(func(d *Data) { d.Friends[0].LastSeenAt = at })
	}
	if !s.Data().Friends[0].LastSeenAt.IsZero() {
		t.Fatal("queued change visible before it was saved")
	}
	if fi, _ := os.Stat(filepath.Join(dir, FileName)); !fi.ModTime().Equal(before) {
		t.Fatal("UpdateLater wrote at once")
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Applied in order: the last change wins.
	if got := s.Data().Friends[0].LastSeenAt; got != time.Unix(50, 0).UTC() {
		t.Fatalf("last seen %v", got)
	}
	s2, _ := Open(dir)
	if got := s2.Data().Friends[0].LastSeenAt; got != time.Unix(50, 0).UTC() {
		t.Fatalf("not persisted: %v", got)
	}
	if err := s.Flush(); err != nil { // nothing queued
		t.Fatal(err)
	}
}

func TestUpdateSavesQueuedChanges(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.UpdateLater(func(d *Data) { d.Identity.Nickname = "queued" })
	s.Update(func(d *Data) error {
		if d.Identity.Nickname != "queued" {
			t.Error("Update did not see the queued change first")
		}
		d.Settings.RelayOnly = true
		return nil
	})
	s2, _ := Open(dir)
	if d := s2.Data(); d.Identity.Nickname != "queued" || !d.Settings.RelayOnly {
		t.Fatalf("not persisted: %+v", d.Identity)
	}
}

func TestUpdateLaterSavesByItself(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.UpdateLater(func(d *Data) { d.Identity.Nickname = "timer" })
	deadline := time.Now().Add(LaterDelay + 3*time.Second)
	for s.Data().Identity.Nickname != "timer" {
		if time.Now().After(deadline) {
			t.Fatal("queued change was never saved")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// UpdateLater must not wait for a save in progress (it runs on the socket
// read goroutine).
func TestUpdateLaterDoesNotBlockOnSave(t *testing.T) {
	s, _ := Open(t.TempDir())
	started, release, updated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(updated)
		s.Update(func(d *Data) error {
			close(started)
			<-release // holds the store lock like a slow disk write
			return nil
		})
	}()
	<-started
	done := make(chan struct{})
	go func() { s.UpdateLater(func(d *Data) {}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("UpdateLater blocked behind Update")
	}
	close(release)
	// Let the save finish (and the queued change with it) before the
	// temporary directory is removed.
	<-updated
	s.Flush()
}
