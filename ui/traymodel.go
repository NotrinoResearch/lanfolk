package main

import (
	"fmt"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// Tray icon names; each has an .ico in ui/assets.
const (
	trayOnline     = "online"     // in a network and connected
	trayConnecting = "connecting" // in a network, not connected yet
	trayOffline    = "offline"    // not in any network
	trayAttention  = "attention"  // the service is down or the adapter failed
)

// traySnapshot is the latest state the UI received from the service.
type traySnapshot struct {
	Connected bool
	Status    *ipc.Status
	Networks  []ipc.Network
	Peers     []ipc.Peer
}

// trayModel is what the tray icon and its menu show.
type trayModel struct {
	Icon            string
	Tooltip         string
	Status          string // first, disabled line of the menu
	IP              string // our virtual IP in the active network
	Networks        []trayNetwork
	ActiveNetworkID string
	CanInvite       bool // we own the active network
}

type trayNetwork struct {
	ID     string
	Name   string
	Active bool
}

// trayView derives the tray model from a snapshot. It is pure so that it can
// be tested without a tray.
func trayView(s traySnapshot) trayModel {
	if !s.Connected || s.Status == nil {
		return trayModel{Icon: trayAttention, Status: "The Lanfolk service is not running",
			Tooltip: "Lanfolk\nThe service is not running"}
	}
	st := s.Status
	m := trayModel{IP: st.VirtualIP}
	var active *ipc.Network
	for i := range s.Networks {
		n := &s.Networks[i]
		m.Networks = append(m.Networks, trayNetwork{ID: n.ID, Name: n.Name, Active: n.Active})
		if n.Active {
			active = n
		}
	}
	switch {
	case active == nil:
		m.Icon, m.Status, m.IP = trayOffline, "Not connected to a network", ""
	case st.AdapterError != "":
		m.Icon, m.Status = trayAttention, "Network adapter problem: open Lanfolk"
	default:
		m.ActiveNetworkID = active.ID
		m.CanInvite = active.IsOwner
		others, online := 0, 0
		for _, mem := range active.Members {
			if !mem.IsSelf {
				others++
			}
		}
		for _, p := range s.Peers {
			if p.Online {
				online++
			}
		}
		// Counts include you, like the member list in the window.
		if others == 0 {
			m.Status = active.Name + " · waiting for friends"
		} else {
			m.Status = fmt.Sprintf("%s · %d of %d online", active.Name, online+1, others+1)
		}
		m.Icon = trayOnline
		if !st.AdapterUp || (online == 0 && others > 0 && !anyRelayUp(st.Relays)) {
			m.Icon = trayConnecting
		}
	}
	m.Tooltip = truncateRunes("Lanfolk\n"+m.Status, 127)
	return m
}

func anyRelayUp(rs []ipc.RelayStatus) bool {
	for _, r := range rs {
		if r.Mode == "udp" || r.Mode == "tcp" {
			return true
		}
	}
	return false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
