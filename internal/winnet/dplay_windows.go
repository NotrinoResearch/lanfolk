//go:build windows

package winnet

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows/registry"
)

// DirectPlay's NAT helper (dpnathlp.dll, loaded by the DirectPlay TCP/IP
// service provider) asks the default gateway over UPnP for its external
// address, maps a port there and writes that address and port into
// DirectPlay messages as the address to answer. Over a virtual LAN the other
// members then try to reach the router of a member, not the member, and a
// member behind a UPnP router never sees or joins a session (D-038).
//
// The helper reads the per-user value
// HKCU\Software\Microsoft\DirectPlayNATHelp\DPNHUPnP\UPnPMode: 1 always use
// UPnP, 2 never, anything else (the default) use it unless the SSDP or UPnP
// host service is disabled.
const (
	// DirectPlayNATKey is the helper's settings key below a user's hive.
	DirectPlayNATKey   = `Software\Microsoft\DirectPlayNATHelp\DPNHUPnP`
	directPlayUPnPMode = "UPnPMode"
	directPlayUPnPOff  = 2
)

// DirectPlayGuard keeps DirectPlay's UPnP off for the owner account while a
// network is active and restores the account's own value afterwards. The
// value it replaced is kept in a state file, so a restart in between still
// restores it.
type DirectPlayGuard struct {
	Root  registry.Key  // registry.USERS in the service
	SID   func() string // the owner account; "" while unknown
	State string        // state file path
	Log   *slog.Logger

	mu     sync.Mutex
	logged bool // whether "not loaded" was logged for the current wait
}

// dplayState is the state file: the account and the value Lanfolk replaced.
type dplayState struct {
	SID         string `json:"sid"`
	PrevPresent bool   `json:"prev_present"`
	PrevValue   uint32 `json:"prev_value"`
}

// Sync holds UPnPMode at "never" for the owner while active is true and
// restores the replaced value otherwise. It is cheap when nothing changes and
// may be called every few seconds; an account whose registry is not loaded
// (signed out) is handled on a later call.
func (g *DirectPlayGuard) Sync(active bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, held := g.load()
	sid := ""
	if g.SID != nil {
		sid = g.SID()
	}
	if held && (!active || st.SID != sid) {
		if !g.restore(st) {
			return // the account is signed out; try again later
		}
		held = false
	}
	if !active || sid == "" {
		return
	}
	if !g.loaded(sid) {
		if !g.logged {
			g.log("DirectPlay setting waits until the account signs in", "sid", sid)
			g.logged = true
		}
		return
	}
	g.logged = false
	cur, present, err := g.read(sid)
	if err != nil {
		g.log("reading the DirectPlay setting failed", "err", err)
		return
	}
	if held {
		if !present || cur != directPlayUPnPOff {
			g.write(sid) // someone reset it while the network is active
		}
		return
	}
	if err := g.save(dplayState{SID: sid, PrevPresent: present, PrevValue: cur}); err != nil {
		g.log("saving the DirectPlay state failed; leaving the setting alone", "err", err)
		return
	}
	if g.write(sid) {
		g.log("DirectPlay UPnP off while a network is active", "previous", describe(present, cur))
	}
}

// restore puts back the value Lanfolk replaced, unless the account changed it
// since. It reports false when the account's registry is not loaded.
func (g *DirectPlayGuard) restore(st dplayState) bool {
	if !g.loaded(st.SID) {
		return false
	}
	cur, present, err := g.read(st.SID)
	if err != nil {
		g.log("reading the DirectPlay setting failed", "err", err)
		return false
	}
	if present && cur == directPlayUPnPOff {
		k, err := registry.OpenKey(g.Root, st.SID+`\`+DirectPlayNATKey, registry.SET_VALUE)
		if err != nil {
			g.log("restoring the DirectPlay setting failed", "err", err)
			return false
		}
		if st.PrevPresent {
			err = k.SetDWordValue(directPlayUPnPMode, st.PrevValue)
		} else {
			err = k.DeleteValue(directPlayUPnPMode)
		}
		k.Close()
		if err != nil {
			g.log("restoring the DirectPlay setting failed", "err", err)
			return false
		}
		g.log("DirectPlay setting restored", "value", describe(st.PrevPresent, st.PrevValue))
	}
	if err := os.Remove(g.State); err != nil && !errors.Is(err, os.ErrNotExist) {
		g.log("removing the DirectPlay state failed", "err", err)
		return false
	}
	return true
}

// loaded reports whether the account's registry hive is available.
func (g *DirectPlayGuard) loaded(sid string) bool {
	k, err := registry.OpenKey(g.Root, sid, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func (g *DirectPlayGuard) read(sid string) (v uint32, present bool, err error) {
	k, err := registry.OpenKey(g.Root, sid+`\`+DirectPlayNATKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer k.Close()
	x, _, err := k.GetIntegerValue(directPlayUPnPMode)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return uint32(x), true, nil
}

func (g *DirectPlayGuard) write(sid string) bool {
	k, _, err := registry.CreateKey(g.Root, sid+`\`+DirectPlayNATKey, registry.SET_VALUE)
	if err == nil {
		err = k.SetDWordValue(directPlayUPnPMode, directPlayUPnPOff)
		k.Close()
	}
	if err != nil {
		g.log("turning DirectPlay UPnP off failed", "err", err)
		return false
	}
	return true
}

func (g *DirectPlayGuard) load() (dplayState, bool) {
	var st dplayState
	b, err := os.ReadFile(g.State)
	if err != nil || json.Unmarshal(b, &st) != nil || st.SID == "" {
		return st, false
	}
	return st, true
}

func (g *DirectPlayGuard) save(st dplayState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := g.State + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.State)
}

func (g *DirectPlayGuard) log(msg string, args ...any) {
	if g.Log != nil {
		g.Log.Info(msg, args...)
	}
}

func describe(present bool, v uint32) any {
	if !present {
		return "not set"
	}
	return v
}

// DirectPlayStateFile is the state file name in the data folder.
func DirectPlayStateFile(dataDir string) string {
	return filepath.Join(dataDir, "directplay-upnp.json")
}
