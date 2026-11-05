//go:build windows

package winnet

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// testHive creates HKCU\Software\LanfolkTest\<unique> as a stand-in for a
// user's hive below registry.USERS, and removes it after the test.
func testHive(t *testing.T) string {
	t.Helper()
	sid := fmt.Sprintf(`Software\LanfolkTest\hive-%d`, time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, sid, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, sid+`\`+DirectPlayNATKey)
		for _, p := range []string{`Software\Microsoft\DirectPlayNATHelp`, `Software\Microsoft`, `Software`, ""} {
			key := sid
			if p != "" {
				key += `\` + p
			}
			registry.DeleteKey(registry.CURRENT_USER, key)
		}
		registry.DeleteKey(registry.CURRENT_USER, `Software\LanfolkTest`) // only when empty
	})
	return sid
}

func newGuard(t *testing.T, sid string) *DirectPlayGuard {
	return &DirectPlayGuard{Root: registry.CURRENT_USER, SID: func() string { return sid },
		State: filepath.Join(t.TempDir(), "directplay-upnp.json")}
}

func setMode(t *testing.T, sid string, v uint32) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, sid+`\`+DirectPlayNATKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.SetDWordValue(directPlayUPnPMode, v); err != nil {
		t.Fatal(err)
	}
}

func mode(t *testing.T, g *DirectPlayGuard, sid string) string {
	v, present, err := g.read(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		return "unset"
	}
	return fmt.Sprint(v)
}

func TestDirectPlayGuardUnsetValue(t *testing.T) {
	sid := testHive(t)
	g := newGuard(t, sid)
	g.Sync(true)
	if got := mode(t, g, sid); got != "2" {
		t.Fatalf("active: UPnPMode %s, want 2", got)
	}
	g.Sync(true) // nothing changes
	g.Sync(false)
	if got := mode(t, g, sid); got != "unset" {
		t.Fatalf("inactive: UPnPMode %s, want it removed again", got)
	}
	if _, held := g.load(); held {
		t.Fatal("state file kept after restoring")
	}
}

func TestDirectPlayGuardRestoresTheUsersValueAfterARestart(t *testing.T) {
	sid := testHive(t)
	setMode(t, sid, 1)
	g := newGuard(t, sid)
	g.Sync(true)
	if got := mode(t, g, sid); got != "2" {
		t.Fatalf("active: UPnPMode %s, want 2", got)
	}
	// The service restarts while the network is active, then the network ends.
	g2 := &DirectPlayGuard{Root: g.Root, SID: g.SID, State: g.State}
	g2.Sync(false)
	if got := mode(t, g2, sid); got != "1" {
		t.Fatalf("restored UPnPMode %s, want the user's 1", got)
	}
}

func TestDirectPlayGuardKeepsItOffWhileActive(t *testing.T) {
	sid := testHive(t)
	g := newGuard(t, sid)
	g.Sync(true)
	setMode(t, sid, 0) // something resets it while a network is active
	g.Sync(true)
	if got := mode(t, g, sid); got != "2" {
		t.Fatalf("UPnPMode %s after a reset, want 2", got)
	}
	g.Sync(false)
	if got := mode(t, g, sid); got != "unset" {
		t.Fatalf("inactive: UPnPMode %s, want unset", got)
	}
}

func TestDirectPlayGuardLeavesAChangedValueAlone(t *testing.T) {
	sid := testHive(t)
	g := newGuard(t, sid)
	g.Sync(true)
	setMode(t, sid, 1) // the user turns UPnP on deliberately, then the network ends
	g.Sync(false)
	if got := mode(t, g, sid); got != "1" {
		t.Fatalf("UPnPMode %s, want the user's own 1 kept", got)
	}
}

func TestDirectPlayGuardWaitsForTheAccount(t *testing.T) {
	sid := `Software\LanfolkTest\not-signed-in`
	g := newGuard(t, sid)
	g.Sync(true)
	if _, held := g.load(); held {
		t.Fatal("state written for an account whose registry is not loaded")
	}
	if g.loaded(sid) {
		t.Fatal("test hive unexpectedly exists")
	}
}
