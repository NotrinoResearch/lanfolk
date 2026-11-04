//go:build !nomock

package main

// A fake Lanfolk service for previewing the UI: run `Lanfolk.exe -mock`.
// Exclude it from release builds with `-tags nomock`.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// startMock starts the fake service on a private pipe and returns its
// address. Call stop to shut it down.
func startMock() (stop func(), addr string, err error) {
	if runtime.GOOS == "windows" {
		addr = fmt.Sprintf(`\\.\pipe\lanfolk-mock-%d`, os.Getpid())
	} else {
		dir, derr := os.MkdirTemp("", "lanfolk-mock")
		if derr != nil {
			return nil, "", derr
		}
		addr = filepath.Join(dir, "mock.sock")
	}
	ln, err := ipc.Listen(addr)
	if err != nil {
		return nil, "", err
	}
	m := newMockService()
	srv := &ipc.Server{Handler: m.handle}
	m.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	go m.simulate(ctx)
	return func() { cancel(); <-done }, addr, nil
}

type mockService struct {
	srv *ipc.Server

	mu       sync.Mutex
	started  time.Time
	nickname string
	friends  []ipc.Friend
	networks []ipc.Network
	settings ipc.Settings
	pending  map[string]time.Time // friend key -> when the friend "adds us back"
	tick     int
}

const mockSelfKey = "key-ahmet"

func newMockService() *mockService {
	now := time.Now()
	m := &mockService{
		started:  now,
		nickname: "Ahmet",
		pending:  map[string]time.Time{},
		settings: ipc.Settings{
			PortMapping: true, DHTFallback: true,
			CustomRelays: []ipc.Relay{},
			DefaultRelay: "relay.example.net:3478 (UDP) / 443 (TCP)",
		},
		friends: []ipc.Friend{
			{PublicKey: "key-mehmet", FriendCode: mockCode("mehmet"), Nickname: "Mehmet", State: "active", Online: true, AddedAt: now.Add(-72 * time.Hour), LastSeenAt: now},
			{PublicKey: "key-veli", FriendCode: mockCode("veli"), Nickname: "Veli", State: "active", Online: true, AddedAt: now.Add(-48 * time.Hour), LastSeenAt: now},
			{PublicKey: "key-ayse", FriendCode: mockCode("ayse"), Nickname: "Ayse", State: "active", Online: false, AddedAt: now.Add(-24 * time.Hour), LastSeenAt: now.Add(-3 * time.Hour)},
			{PublicKey: "key-can", FriendCode: mockCode("can"), Nickname: "Can", State: "pending", AddedAt: now.Add(-time.Hour)},
		},
	}
	m.networks = []ipc.Network{
		{
			ID: "net-friday", Name: "Friday LAN", OwnerKey: mockSelfKey, IsOwner: true, Active: true, Version: 3,
			Members: []ipc.Member{
				{PublicKey: mockSelfKey, Nickname: "Ahmet", VirtualIP: "10.77.0.2", IsSelf: true, IsOwner: true},
				{PublicKey: "key-mehmet", Nickname: "Mehmet", VirtualIP: "10.77.0.3", IsFriend: true},
				{PublicKey: "key-veli", Nickname: "Veli", VirtualIP: "10.77.0.4", IsFriend: true},
				{PublicKey: "key-ayse", Nickname: "Ayse", VirtualIP: "10.77.0.5", IsFriend: true},
			},
			Relays: []ipc.Relay{{Host: "relay2.example.net", UDPPort: 3478, TCPPort: 443}},
		},
		{
			ID: "net-lunch", Name: "Lunch group", OwnerKey: "key-zeynep", Version: 1,
			Members: []ipc.Member{
				{PublicKey: "key-zeynep", Nickname: "Zeynep", VirtualIP: "10.77.0.2", IsOwner: true},
				{PublicKey: mockSelfKey, Nickname: "Ahmet", VirtualIP: "10.77.0.3", IsSelf: true},
			},
		},
	}
	return m
}

func mockCode(name string) string {
	// Same shape as a real code: prefix plus 58 characters.
	return "lanfolk1" + strings.Repeat(name+"x", 20)[:58]
}

func (m *mockService) active() *ipc.Network {
	for i := range m.networks {
		if m.networks[i].Active {
			return &m.networks[i]
		}
	}
	return nil
}

func (m *mockService) status() ipc.Status {
	up := time.Since(m.started)
	st := ipc.Status{
		Version: "0.0.0-mock", Nickname: m.nickname, PublicKey: mockSelfKey,
		FriendCode: mockCode("ahmet"), ListenPort: 41641, PortMapping: "upnp", DHT: "standby",
		AdapterUp: true, RelayOnly: m.settings.RelayOnly, CompatMode: m.settings.CompatMode,
		Relays: []ipc.RelayStatus{{Name: "relay.example.net:3478", Source: "default", Mode: "udp", RTTMs: 24 + rand.Float64()*3}},
	}
	if n := m.active(); n != nil {
		st.ActiveNetworkID, st.ActiveNetworkName = n.ID, n.Name
		for _, mem := range n.Members {
			if mem.IsSelf {
				st.VirtualIP = mem.VirtualIP
			}
		}
		if len(n.Relays) > 0 {
			// Show how relay states look: the owner's relay switches to the
			// TCP fallback after a while, then goes down.
			r := ipc.RelayStatus{Name: n.Relays[0].Host + ":3478", Source: "network", Mode: "udp", RTTMs: 31}
			switch {
			case up > 90*time.Second:
				r.Mode, r.RTTMs = "down", 0
			case up > 45*time.Second:
				r.Mode, r.RTTMs = "tcp", 88
			}
			st.Relays = append(st.Relays, r)
		}
	}
	for _, c := range m.settings.CustomRelays {
		st.Relays = append(st.Relays, ipc.RelayStatus{Name: fmt.Sprintf("%s:%d", c.Host, c.UDPPort), Source: "custom", Mode: "udp", RTTMs: 40})
	}
	if m.settings.RelayOnly {
		st.Warnings = append(st.Warnings, "Relay only is on: ping may be higher.")
	}
	return st
}

func (m *mockService) friendByKey(key string) *ipc.Friend {
	for i := range m.friends {
		if m.friends[i].PublicKey == key {
			return &m.friends[i]
		}
	}
	return nil
}

func (m *mockService) network(id string) (*ipc.Network, error) {
	for i := range m.networks {
		if m.networks[i].ID == id {
			return &m.networks[i], nil
		}
	}
	return nil, ipc.Errorf(ipc.CodeNotFound, "network not found")
}

func (m *mockService) ownedNetwork(id string) (*ipc.Network, error) {
	n, err := m.network(id)
	if err != nil {
		return nil, err
	}
	if !n.IsOwner {
		return nil, ipc.Errorf(ipc.CodeForbidden, "only the owner can change this network")
	}
	return n, nil
}

func (m *mockService) peers() []ipc.Peer {
	n := m.active()
	if n == nil {
		return []ipc.Peer{}
	}
	out := []ipc.Peer{}
	for i, mem := range n.Members {
		if mem.IsSelf {
			continue
		}
		p := ipc.Peer{PublicKey: mem.PublicKey, Nickname: mem.Nickname, VirtualIP: mem.VirtualIP, Path: "none", Quality: "unknown"}
		if f := m.friendByKey(mem.PublicKey); f != nil {
			p.Online = f.Online
		} else {
			p.Online = true
		}
		if p.Online {
			t := float64(m.tick)
			base := []float64{18, 55, 95, 150}[i%4]
			p.Path = []string{"direct", "member-relay", "shared-relay", "tcp-fallback"}[i%4]
			p.PingMs = base + 6*math.Sin(t/4+float64(i)) + rand.Float64()*3
			p.AvgPingMs = base + 1
			p.JitterMs = []float64{1.2, 6, 14, 35}[i%4] + rand.Float64()
			p.LossPct = []float64{0, 0.2, 1.1, 3.4}[i%4]
			switch p.Path {
			case "member-relay":
				p.RelayDetail = "Mehmet"
			case "shared-relay":
				p.RelayDetail = "relay.example.net:3478"
			case "tcp-fallback":
				p.RelayDetail = "relay.example.net:443"
			}
			switch {
			case p.PingMs > 120 || p.JitterMs > 30 || p.LossPct > 2:
				p.Quality = "poor"
			case p.PingMs <= 60 && p.JitterMs <= 10 && p.LossPct <= 0.5:
				p.Quality = "good"
			default:
				p.Quality = "fair"
			}
		}
		out = append(out, p)
	}
	return out
}

// simulate makes the data change over time.
func (m *mockService) simulate(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.mu.Lock()
		m.tick++
		friendsChanged := false
		for key, at := range m.pending {
			if time.Now().After(at) {
				if f := m.friendByKey(key); f != nil {
					f.State, f.Online, f.LastSeenAt = "active", true, time.Now()
					friendsChanged = true
				}
				delete(m.pending, key)
			}
		}
		if m.tick%20 == 0 { // Veli drops out and comes back
			if f := m.friendByKey("key-veli"); f != nil {
				f.Online = !f.Online
				if !f.Online {
					f.LastSeenAt = time.Now()
				}
				friendsChanged = true
			}
		}
		hasActive := m.active() != nil
		peers, _ := json.Marshal(m.peers())
		st, _ := json.Marshal(m.status())
		friends, _ := json.Marshal(m.friends)
		tick := m.tick
		m.mu.Unlock()
		if hasActive {
			m.srv.Publish(ipc.EventPeers, json.RawMessage(peers))
		}
		if tick%5 == 0 {
			m.srv.Publish(ipc.EventStatus, json.RawMessage(st))
		}
		if friendsChanged {
			m.srv.Publish(ipc.EventFriends, json.RawMessage(friends))
		}
	}
}

// publishAll must be called with the lock held. It encodes the state now and
// sends it after the current request has been answered.
func (m *mockService) publishAll() {
	st, _ := json.Marshal(m.status())
	fr, _ := json.Marshal(m.friends)
	nets, _ := json.Marshal(m.networks)
	go func() {
		m.srv.Publish(ipc.EventStatus, json.RawMessage(st))
		m.srv.Publish(ipc.EventFriends, json.RawMessage(fr))
		m.srv.Publish(ipc.EventNetworks, json.RawMessage(nets))
	}()
}

func (m *mockService) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.dispatch(method, params)
	if err != nil {
		return nil, err
	}
	// Encode while the lock is held: the result shares slices with the state.
	b, err := json.Marshal(res)
	return json.RawMessage(b), err
}

func (m *mockService) dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case ipc.MethodStatus:
		return m.status(), nil
	case ipc.MethodListFriends:
		return m.friends, nil
	case ipc.MethodListNetworks:
		return m.networks, nil
	case ipc.MethodListPeers:
		return m.peers(), nil
	case ipc.MethodGetSettings:
		return m.settings, nil
	case ipc.MethodSetSettings:
		var s ipc.Settings
		if err := ipc.Decode(params, &s); err != nil {
			return nil, err
		}
		if s.ManualPort < 0 || s.ManualPort > 65535 {
			return nil, ipc.Errorf(ipc.CodeInvalid, "port must be between 0 and 65535")
		}
		s.DefaultRelay = m.settings.DefaultRelay
		m.settings = s
		m.publishAll()
		return s, nil
	case ipc.MethodSetNickname:
		var p ipc.NicknameParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Nickname) == "" {
			return nil, ipc.Errorf(ipc.CodeInvalid, "nickname cannot be empty")
		}
		m.nickname = strings.TrimSpace(p.Nickname)
		m.publishAll()
		return true, nil
	case ipc.MethodExportIdentity:
		var p ipc.PasswordParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		return ipc.BackupResult{Backup: "lanfolk-backup-1:MOCKBACKUPDATA-NOT-A-REAL-IDENTITY"}, nil
	case ipc.MethodImportIdentity:
		return true, nil
	case ipc.MethodExportDiagnostics:
		return ipc.DiagnosticsResult{Report: "Lanfolk diagnostics (mock)\nVersion: 0.0.0-mock\nThis report comes from the built-in fake service.\n"}, nil
	case ipc.MethodAddFriend:
		var p ipc.AddFriendParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		code := strings.TrimSpace(p.Code)
		if !strings.HasPrefix(code, "lanfolk1") || len(code) < 20 {
			return nil, ipc.Errorf(ipc.CodeInvalid, "that is not a valid friend code")
		}
		for _, f := range m.friends {
			if f.FriendCode == code {
				return nil, ipc.Errorf(ipc.CodeInvalid, "this friend is already in your list")
			}
		}
		nick := p.Nickname
		if nick == "" {
			nick = "Friend " + code[8:12]
		}
		f := ipc.Friend{PublicKey: "key-" + code[8:20], FriendCode: code, Nickname: nick, State: "pending", AddedAt: time.Now()}
		m.friends = append(m.friends, f)
		m.pending[f.PublicKey] = time.Now().Add(8 * time.Second)
		m.publishAll()
		return f, nil
	case ipc.MethodRemoveFriend:
		var p ipc.KeyParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if m.friendByKey(p.PublicKey) == nil {
			return nil, ipc.Errorf(ipc.CodeNotFound, "friend not found")
		}
		m.friends = slices.DeleteFunc(m.friends, func(f ipc.Friend) bool { return f.PublicKey == p.PublicKey })
		m.publishAll()
		return true, nil
	case ipc.MethodCreateConnCode:
		var p ipc.KeyParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if m.friendByKey(p.PublicKey) == nil {
			return nil, ipc.Errorf(ipc.CodeNotFound, "friend not found")
		}
		return ipc.ConnCodeResult{Code: "LFC-" + strings.ToUpper(fmt.Sprintf("%x", rand.Uint64())), Expires: time.Now().Add(10 * time.Minute)}, nil
	case ipc.MethodUseConnCode:
		var p ipc.CodeParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.TrimSpace(p.Code), "LFC-") {
			return nil, ipc.Errorf(ipc.CodeInvalid, "that connection code is not valid or has expired")
		}
		return true, nil
	case ipc.MethodCreateNetwork:
		var p ipc.CreateNetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, ipc.Errorf(ipc.CodeInvalid, "network name cannot be empty")
		}
		n := ipc.Network{
			ID: fmt.Sprintf("net-%d", rand.Uint32()), Name: strings.TrimSpace(p.Name), OwnerKey: mockSelfKey, IsOwner: true, Version: 1,
			Members: []ipc.Member{{PublicKey: mockSelfKey, Nickname: m.nickname, VirtualIP: "10.77.0.2", IsSelf: true, IsOwner: true}},
		}
		m.networks = append(m.networks, n)
		m.publishAll()
		return n, nil
	case ipc.MethodActivateNetwork:
		var p ipc.NetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.NetworkID != "" {
			if _, err := m.network(p.NetworkID); err != nil {
				return nil, err
			}
		}
		for i := range m.networks {
			m.networks[i].Active = m.networks[i].ID == p.NetworkID
		}
		m.publishAll()
		return true, nil
	case ipc.MethodLeaveNetwork:
		var p ipc.NetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		if _, err := m.network(p.NetworkID); err != nil {
			return nil, err
		}
		m.networks = slices.DeleteFunc(m.networks, func(n ipc.Network) bool { return n.ID == p.NetworkID })
		m.publishAll()
		return true, nil
	case ipc.MethodRenameNetwork:
		var p ipc.RenameParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		n, err := m.ownedNetwork(p.NetworkID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, ipc.Errorf(ipc.CodeInvalid, "network name cannot be empty")
		}
		n.Name = strings.TrimSpace(p.Name)
		n.Version++
		m.publishAll()
		return true, nil
	case ipc.MethodSetNetworkRelays:
		var p ipc.SetRelaysParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		n, err := m.ownedNetwork(p.NetworkID)
		if err != nil {
			return nil, err
		}
		if len(p.Relays) > 3 {
			return nil, ipc.Errorf(ipc.CodeInvalid, "a network can have at most 3 relays")
		}
		n.Relays = p.Relays
		n.Version++
		m.publishAll()
		return true, nil
	case ipc.MethodAddMember:
		var p ipc.MemberParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		n, err := m.ownedNetwork(p.NetworkID)
		if err != nil {
			return nil, err
		}
		f := m.friendByKey(p.PublicKey)
		if f == nil || f.State != "active" {
			return nil, ipc.Errorf(ipc.CodeInvalid, "only active friends can be added")
		}
		for _, mem := range n.Members {
			if mem.PublicKey == p.PublicKey {
				return nil, ipc.Errorf(ipc.CodeInvalid, "%s is already a member", f.Nickname)
			}
		}
		n.Members = append(n.Members, ipc.Member{PublicKey: f.PublicKey, Nickname: f.Nickname, VirtualIP: fmt.Sprintf("10.77.0.%d", len(n.Members)+2), IsFriend: true})
		n.Version++
		m.publishAll()
		return true, nil
	case ipc.MethodRemoveMember:
		var p ipc.MemberParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		n, err := m.ownedNetwork(p.NetworkID)
		if err != nil {
			return nil, err
		}
		n.Members = slices.DeleteFunc(n.Members, func(mem ipc.Member) bool { return mem.PublicKey == p.PublicKey && !mem.IsSelf })
		n.Version++
		m.publishAll()
		return true, nil
	}
	return nil, ipc.Errorf(ipc.CodeInvalid, "unknown method %q", method)
}
