package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/tunwrap"
	"github.com/NotrinoResearch/lanfolk/internal/version"
)

// AddFriend adds a friend by code. The friendship is pending until the
// friend adds us too.
func (e *Engine) AddFriend(code, nickname string) (ipc.Friend, error) {
	pk, err := identity.ParseFriendCode(code)
	if err != nil {
		return ipc.Friend{}, ipc.Errorf(ipc.CodeInvalid, "that friend code is not valid; check that it was copied completely")
	}
	if pk == e.key.Public() {
		return ipc.Friend{}, ipc.Errorf(ipc.CodeInvalid, "that is your own friend code")
	}
	nickname = cleanText(nickname, network.MaxNicknameLen)
	err = e.store.Update(func(d *store.Data) error {
		for i := range d.Friends {
			f := &d.Friends[i]
			if f.PublicKey == pk.Hex() {
				if f.ViaInvite {
					// Added by an invite before; now the user wants to keep
					// this friend for good.
					f.ViaInvite = false
					if nickname != "" {
						f.Nickname = nickname
					}
					return nil
				}
				return ipc.Errorf(ipc.CodeInvalid, "this friend is already in your list")
			}
		}
		d.Friends = append(d.Friends, store.Friend{PublicKey: pk.Hex(), Nickname: nickname, AddedAt: time.Now().UTC()})
		return nil
	})
	if err != nil {
		return ipc.Friend{}, err
	}
	e.mu.Lock()
	e.reconcileLocked()
	p := e.peers[pk]
	e.mu.Unlock()
	if p != nil {
		go e.sendHello(p)
	}
	e.publishFriends()
	for _, f := range e.Friends() {
		if f.PublicKey == pk.Hex() {
			return f, nil
		}
	}
	return ipc.Friend{}, ipc.Errorf(ipc.CodeInternal, "friend was not stored")
}

// RemoveFriend removes a friend. Network membership is not changed.
func (e *Engine) RemoveFriend(pk identity.PublicKey) error {
	found := false
	err := e.store.Update(func(d *store.Data) error {
		out := d.Friends[:0]
		for _, f := range d.Friends {
			if f.PublicKey == pk.Hex() {
				found = true
				continue
			}
			out = append(out, f)
		}
		d.Friends = out
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return ipc.Errorf(ipc.CodeNotFound, "not a friend")
	}
	e.mu.Lock()
	e.reconcileLocked()
	e.mu.Unlock()
	e.publishFriends()
	return nil
}

func cleanText(s string, max int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	for len(s) > max {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// SetNickname changes the display nickname announced to peers.
func (e *Engine) SetNickname(n string) error {
	n = cleanText(n, network.MaxNicknameLen)
	if n == "" {
		return ipc.Errorf(ipc.CodeInvalid, "nickname cannot be empty")
	}
	if err := e.store.Update(func(d *store.Data) error { d.Identity.Nickname = n; return nil }); err != nil {
		return err
	}
	e.mu.Lock()
	e.nick = n
	peers := e.peerListLocked()
	e.mu.Unlock()
	for _, p := range peers {
		go e.sendHello(p)
	}
	e.publishStatus()
	go e.publishNetworks() // our own row in member lists
	return nil
}

// Friends lists friends with presence.
func (e *Engine) Friends() []ipc.Friend {
	d := e.store.Data()
	now := time.Now()
	out := make([]ipc.Friend, 0, len(d.Friends))
	for _, f := range d.Friends {
		pk, err := identity.ParsePublicKeyHex(f.PublicKey)
		if err != nil {
			continue
		}
		fr := ipc.Friend{PublicKey: f.PublicKey, FriendCode: pk.FriendCode(), Nickname: f.Nickname,
			State: "pending", AddedAt: f.AddedAt, LastSeenAt: f.LastSeenAt}
		if !f.ConfirmedAt.IsZero() {
			fr.State = "active"
		}
		e.mu.Lock()
		p := e.peers[pk]
		e.mu.Unlock()
		if p != nil {
			fr.Online = p.online(now, e.cfg.OfflineAfter)
			// The nickname the friend announces wins over the stored one,
			// which was typed when adding or copied at join time.
			p.mu.Lock()
			live := p.nick
			p.mu.Unlock()
			if live != "" {
				fr.Nickname = live
			} else if fr.Nickname == "" {
				fr.Nickname = p.displayName()
			}
		}
		out = append(out, fr)
	}
	return out
}

// Networks lists the stored networks.
//
// The signed member list keeps the nickname a member had when it joined.
// Members show their current nickname instead: our own, or the one a peer
// last announced in Hello, which is remembered across restarts. The list
// nickname is the fallback for members that never announced one to us.
func (e *Engine) Networks() []ipc.Network {
	type named struct {
		net, mem int
		p        *peer
	}
	var live []named
	known := e.store.KnownNames()
	e.mu.Lock()
	me := e.key.Public()
	nick := e.nick
	out := make([]ipc.Network, 0, len(e.lists))
	for _, l := range e.lists {
		n := ipc.Network{ID: l.ID.String(), Name: l.Name, OwnerKey: l.Owner.Hex(), IsOwner: l.Owner == me,
			Active: e.active != nil && e.active.ID == l.ID, Version: l.Version}
		for _, m := range l.Members {
			p := e.peers[m.PublicKey]
			mem := ipc.Member{PublicKey: m.PublicKey.Hex(), Nickname: m.Nickname,
				VirtualIP: m.VirtualIP.String(), IsSelf: m.PublicKey == me, IsOwner: m.PublicKey == l.Owner,
				IsFriend: p != nil && p.isFriend && p.confirmed}
			if k := known[mem.PublicKey]; k != "" && !mem.IsSelf {
				mem.Nickname = k
			}
			if mem.IsSelf && nick != "" {
				mem.Nickname = nick
			} else if p != nil {
				live = append(live, named{len(out), len(n.Members), p})
			}
			n.Members = append(n.Members, mem)
		}
		for _, r := range l.Relays {
			n.Relays = append(n.Relays, ipc.Relay{Host: r.Host, UDPPort: int(r.UDPPort), TCPPort: int(r.TCPPort)})
		}
		out = append(out, n)
	}
	e.mu.Unlock()
	// p.mu is taken after e.mu is released.
	for _, x := range live {
		x.p.mu.Lock()
		n := x.p.nick
		x.p.mu.Unlock()
		if n != "" {
			out[x.net].Members[x.mem].Nickname = n
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Peers lists live connection data for the active network's members.
func (e *Engine) Peers() []ipc.Peer {
	now := time.Now()
	e.mu.Lock()
	var peers []*peer
	for _, p := range e.peers {
		if p.managed {
			peers = append(peers, p)
		}
	}
	e.mu.Unlock()
	out := make([]ipc.Peer, 0, len(peers))
	for _, p := range peers {
		// vip is written under e.mu by reconcile; read the lock-free copy.
		var vip netip.Addr
		if v := p.vipp.Load(); v != nil {
			vip = *v
		}
		p.mu.Lock()
		appVersion := p.appVersion
		p.mu.Unlock()
		ip := ipc.Peer{PublicKey: p.key.Hex(), Nickname: p.displayName(), VirtualIP: vip.String(),
			Online: p.online(now, e.cfg.OfflineAfter), Path: "none", Quality: pathmgr.QualityUnknown.String(),
			Version: appVersion}
		// An offline member keeps an optimistic relay path for its first
		// packets; do not report it as a connection.
		if v := p.active.Load(); v != nil && ip.Online {
			ip.Path = v.kind.String()
			p.mu.Lock()
			var info pathmgr.PathInfo
			if p.pm != nil {
				info, _ = p.pm.Path(v.pathID)
			}
			p.mu.Unlock()
			ip.RelayDetail = e.pathDetail(info)
			if v.kind == pathmgr.Direct {
				ip.RelayDetail = ""
			}
		}
		if st := p.stats.Load(); st != nil && st.Samples > 0 {
			ip.PingMs, ip.AvgPingMs, ip.JitterMs = ms(st.LatestRTT), ms(st.AvgRTT), ms(st.Jitter)
			ip.LossPct = st.Loss * 100
			ip.Quality = st.Quality.String()
		}
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VirtualIP < out[j].VirtualIP })
	return out
}

// adapterWarningPrefix starts the adapter warning; the desktop app shows the
// adapter error in its own banner and skips this one.
const adapterWarningPrefix = "The virtual network adapter is not available: "

// Status describes the engine for the UI.
func (e *Engine) Status() ipc.Status {
	e.mu.Lock()
	st := ipc.Status{Version: version.Version, Nickname: e.nick, PublicKey: e.key.Public().Hex(),
		FriendCode: e.key.Public().FriendCode(), ListenPort: int(e.sock.Port()),
		RelayOnly: e.settings.RelayOnly, CompatMode: e.settings.CompatMode, AdapterError: e.tunErr}
	if e.active != nil {
		st.ActiveNetworkID, st.ActiveNetworkName = e.active.ID.String(), e.active.Name
		if m, err := e.selfAddressLocked(); err == nil {
			st.VirtualIP = m.VirtualIP.String()
		}
	}
	st.AdapterUp = e.tunnel != nil
	for _, r := range e.relays {
		rs := ipc.RelayStatus{Name: r.name(), Source: r.source, Mode: r.client.Mode().String(), RTTMs: ms(r.client.RTT())}
		st.Relays = append(st.Relays, rs)
	}
	if len(e.relays) == 0 {
		st.Warnings = append(st.Warnings, "No relay server is set up, so only friends with a direct connection can reach you. Add one in Settings, Relays.")
	}
	pm := e.portmap
	dhtEnabled := e.settings.DHTFallback
	e.mu.Unlock()
	if pm != nil {
		if m, ok := pm.m.Current(); ok {
			st.PortMapping = string(m.Protocol)
		}
	}
	st.DHT = "off"
	if e.dht != nil {
		st.DHT = e.dht.state(dhtEnabled)
	}
	if runtime.GOOS == "windows" && st.AdapterError != "" {
		st.Warnings = append(st.Warnings, adapterWarningPrefix+st.AdapterError)
	}
	return st
}

// Settings returns the user settings.
func (e *Engine) Settings() ipc.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := ipc.Settings{RelayOnly: e.settings.RelayOnly, CompatMode: e.settings.CompatMode, ManualPort: e.settings.ManualPort,
		PortMapping: e.settings.PortMapping, DHTFallback: e.settings.DHTFallback,
		Notifications: !e.settings.NotificationsOff, CheckUpdates: !e.settings.UpdateCheckOff,
		AllowServices: e.settings.AllowServices}
	for _, r := range e.settings.CustomRelays {
		s.CustomRelays = append(s.CustomRelays, ipc.Relay{Host: r.Host, UDPPort: r.UDPPort, TCPPort: r.TCPPort})
	}
	if host, udp, tcp, ok := version.DefaultRelay(); ok && e.cfg.DefaultRelays == nil {
		s.DefaultRelay = fmt.Sprintf("%s (UDP %d, TCP %d)", host, udp, tcp)
	} else if len(e.cfg.DefaultRelays) > 0 {
		r := e.cfg.DefaultRelays[0]
		s.DefaultRelay = fmt.Sprintf("%s (UDP %d, TCP %d)", r.Host, r.UDPPort, r.TCPPort)
	}
	return s
}

// SetSettings validates and applies settings. A manual port change needs a
// service restart; everything else applies immediately.
func (e *Engine) SetSettings(s ipc.Settings) (ipc.Settings, error) {
	if s.ManualPort < 0 || s.ManualPort > 65535 {
		return ipc.Settings{}, ipc.Errorf(ipc.CodeInvalid, "the manual port must be between 1 and 65535, or 0 for automatic")
	}
	if len(s.CustomRelays) > MaxActiveRelays {
		return ipc.Settings{}, ipc.Errorf(ipc.CodeInvalid, "at most %d custom relays", MaxActiveRelays)
	}
	var relays []store.Relay
	for _, r := range s.CustomRelays {
		nr := network.Relay{Host: strings.TrimSpace(r.Host), UDPPort: uint16(r.UDPPort), TCPPort: uint16(r.TCPPort)}
		if r.UDPPort <= 0 || r.UDPPort > 65535 || r.TCPPort < 0 || r.TCPPort > 65535 || nr.Validate() != nil {
			return ipc.Settings{}, ipc.Errorf(ipc.CodeInvalid, "invalid relay %q", r.Host)
		}
		relays = append(relays, store.Relay{Host: nr.Host, UDPPort: r.UDPPort, TCPPort: r.TCPPort})
	}
	err := e.store.Update(func(d *store.Data) error {
		d.Settings.RelayOnly, d.Settings.CompatMode, d.Settings.ManualPort = s.RelayOnly, s.CompatMode, s.ManualPort
		d.Settings.PortMapping, d.Settings.DHTFallback, d.Settings.CustomRelays = s.PortMapping, s.DHTFallback, relays
		d.Settings.NotificationsOff, d.Settings.UpdateCheckOff = !s.Notifications, !s.CheckUpdates
		d.Settings.AllowServices = s.AllowServices
		return nil
	})
	if err != nil {
		return ipc.Settings{}, err
	}
	e.mu.Lock()
	relayOnlyChanged := e.settings.RelayOnly != s.RelayOnly
	e.settings = e.store.Data().Settings
	e.syncSettingsCopies()
	var hello []*peer
	if relayOnlyChanged {
		cfg := e.pathConfig()
		for _, p := range e.peers {
			p.mu.Lock()
			if p.pm != nil {
				p.pm.SetConfig(cfg)
			}
			p.candSentAt = time.Time{} // candidates go out again right away
			p.mu.Unlock()
			hello = append(hello, p)
		}
		e.rebuildAddrMap()
	}
	e.setCompatModeLocked(s.CompatMode)
	e.applyRelaysLocked()
	e.mu.Unlock()
	// The Hello flag tells members right away, so they stop (or resume)
	// using our direct address instead of waiting for the next Hello.
	for _, p := range hello {
		go e.sendHello(p)
	}
	go e.publishStatus()
	return e.Settings(), nil
}

// ExportIdentity returns a password-protected backup.
func (e *Engine) ExportIdentity(password string) (string, error) {
	if len(password) < 8 {
		return "", ipc.Errorf(ipc.CodeInvalid, "use a password of at least 8 characters")
	}
	e.mu.Lock()
	nick := e.nick
	e.mu.Unlock()
	return identity.ExportBackup(e.key, nick, password)
}

// ImportIdentity replaces the identity with a backup. Friends and networks
// stay; the service must restart to use the new key.
func (e *Engine) ImportIdentity(backup, password string) error {
	k, nick, err := identity.ImportBackup(backup, password)
	if err != nil {
		return ipc.Errorf(ipc.CodeInvalid, "%v", err)
	}
	blob, err := e.cfg.Protector.Protect(k.Seed())
	if err != nil {
		return err
	}
	return e.store.Update(func(d *store.Data) error {
		d.Identity = store.Identity{PublicKey: k.Public().Hex(), EncryptedPrivateKey: blob, Nickname: nick}
		return nil
	})
}

// Diagnostics returns a JSON report for troubleshooting. Users attach it to
// public bug reports, so public IP addresses are shortened, keys are cut to
// key prefixes and the friend code is left out (see redact.go).
func (e *Engine) Diagnostics() string {
	type pathDump struct {
		Kind, Source, Addr, Via string
		Alive, Active           bool
		SRTTms                  float64
	}
	type peerDump struct {
		Key, Nickname, VirtualIP string
		Friend, Confirmed        bool
		Managed, Online          bool
		Paths                    []pathDump
	}
	now := time.Now()
	rep := struct {
		Generated  time.Time
		OS         string
		Status     ipc.Status
		Settings   ipc.Settings
		Candidates []string
		Peers      []peerDump
		// Tunnel counts the virtual LAN's packets, including what the
		// filters dropped (DroppedBlocked, DroppedService).
		Tunnel *tunwrap.Stats `json:",omitempty"`
	}{Generated: now.UTC(), OS: runtime.GOOS + "/" + runtime.GOARCH, Status: e.Status(), Settings: e.Settings()}
	redactStatus(&rep.Status)
	for i := range rep.Settings.CustomRelays {
		rep.Settings.CustomRelays[i].Host = redactHostPort(rep.Settings.CustomRelays[i].Host)
	}
	rep.Settings.DefaultRelay = redactHostPort(rep.Settings.DefaultRelay)
	e.mu.Lock()
	for _, c := range e.cands {
		rep.Candidates = append(rep.Candidates, c.source.String()+" "+redactAddrPort(c.addr))
	}
	if e.tunnel != nil && e.tunnel.wrap != nil {
		st := e.tunnel.wrap.Stats()
		rep.Tunnel = &st
	}
	peers := e.peerListLocked()
	e.mu.Unlock()
	for _, p := range peers {
		e.mu.Lock()
		d := peerDump{Key: p.key.String(), Nickname: p.displayName(), VirtualIP: p.vip.String(),
			Friend: p.isFriend, Confirmed: p.confirmed, Managed: p.managed}
		e.mu.Unlock()
		d.Online = p.online(now, e.cfg.OfflineAfter)
		p.mu.Lock()
		if p.pm != nil {
			for _, pi := range p.pm.Paths(nanotime()) {
				d.Paths = append(d.Paths, pathDump{Kind: pi.Kind.String(), Source: pi.Source.String(), Addr: redactAddrPort(pi.Addr),
					Via: pi.Via.String(), Alive: pi.Alive, Active: pi.Active, SRTTms: ms(pi.SRTT)})
			}
		}
		p.mu.Unlock()
		rep.Peers = append(rep.Peers, d)
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	return string(b)
}

// NetworkActive reports whether a network is active (the tunnel is wanted).
func (e *Engine) NetworkActive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active != nil
}

// OwnerSID returns the account allowed to use the control pipe.
func (e *Engine) OwnerSID() string { return e.store.Data().OwnerSID }

// ClaimOwner records sid as the pipe owner if none is recorded yet and
// reports whether sid is the owner.
func (e *Engine) ClaimOwner(sid string) (bool, error) {
	owner := ""
	err := e.store.Update(func(d *store.Data) error {
		if d.OwnerSID == "" {
			d.OwnerSID = sid
		}
		owner = d.OwnerSID
		return nil
	})
	return owner == sid, err
}

// The publish functions build a snapshot only when somebody listens; the UI
// reads all of them again when it subscribes.

func (e *Engine) publishFriends() {
	if e.wantEvents() {
		e.emit(ipc.EventFriends, e.Friends())
	}
}

func (e *Engine) publishNetworks() {
	if e.wantEvents() {
		e.emit(ipc.EventNetworks, e.Networks())
	}
}

func (e *Engine) publishStatus() {
	if e.wantEvents() {
		e.emit(ipc.EventStatus, e.Status())
	}
}

func (e *Engine) publishPeers() {
	if !e.wantEvents() {
		return
	}
	e.mu.Lock()
	active := e.active != nil
	e.mu.Unlock()
	if active {
		e.emit(ipc.EventPeers, e.Peers())
	}
}

func parseKey(s string) (identity.PublicKey, error) {
	if pk, err := identity.ParsePublicKeyHex(s); err == nil {
		return pk, nil
	}
	if pk, err := identity.ParseFriendCode(s); err == nil {
		return pk, nil
	}
	return identity.PublicKey{}, ipc.Errorf(ipc.CodeInvalid, "invalid public key")
}

func parseNetID(s string) (network.ID, error) {
	id, err := network.ParseID(s)
	if err != nil {
		return id, ipc.Errorf(ipc.CodeInvalid, "invalid network id")
	}
	return id, nil
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotOwner):
		return ipc.Errorf(ipc.CodeForbidden, "%v", err)
	case errors.Is(err, ErrUnknownNetwork):
		return ipc.Errorf(ipc.CodeNotFound, "%v", err)
	case errors.Is(err, ErrNotFriend):
		return ipc.Errorf(ipc.CodeInvalid, "%v", err)
	}
	var ie *ipc.Error
	if errors.As(err, &ie) {
		return ie
	}
	return ipc.Errorf(ipc.CodeInvalid, "%v", err)
}

// Handle serves one IPC request.
func (e *Engine) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case ipc.MethodStatus:
		return e.Status(), nil
	case ipc.MethodSetNickname:
		var p ipc.NicknameParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		return true, mapErr(e.SetNickname(p.Nickname))
	case ipc.MethodExportIdentity:
		var p ipc.PasswordParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		b, err := e.ExportIdentity(p.Password)
		return ipc.BackupResult{Backup: b}, mapErr(err)
	case ipc.MethodImportIdentity:
		var p ipc.ImportParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		return true, mapErr(e.ImportIdentity(p.Backup, p.Password))
	case ipc.MethodListFriends:
		return e.Friends(), nil
	case ipc.MethodAddFriend:
		var p ipc.AddFriendParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		f, err := e.AddFriend(p.Code, p.Nickname)
		return f, mapErr(err)
	case ipc.MethodRemoveFriend:
		var p ipc.KeyParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		pk, err := parseKey(p.PublicKey)
		if err != nil {
			return nil, err
		}
		return true, mapErr(e.RemoveFriend(pk))
	case ipc.MethodListNetworks:
		return e.Networks(), nil
	case ipc.MethodCreateNetwork:
		var p ipc.CreateNetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		l, err := e.CreateNetwork(cleanText(p.Name, network.MaxNameLen))
		if err != nil {
			return nil, mapErr(err)
		}
		for _, n := range e.Networks() {
			if n.ID == l.ID.String() {
				return n, nil
			}
		}
		return nil, ipc.Errorf(ipc.CodeInternal, "network not stored")
	case ipc.MethodAddMember, ipc.MethodRemoveMember:
		var p ipc.MemberParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		id, err := parseNetID(p.NetworkID)
		if err != nil {
			return nil, err
		}
		pk, err := parseKey(p.PublicKey)
		if err != nil {
			return nil, err
		}
		if method == ipc.MethodAddMember {
			return true, mapErr(e.AddMember(id, pk))
		}
		return true, mapErr(e.RemoveMember(id, pk))
	case ipc.MethodRenameNetwork:
		var p ipc.RenameParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		id, err := parseNetID(p.NetworkID)
		if err != nil {
			return nil, err
		}
		return true, mapErr(e.RenameNetwork(id, cleanText(p.Name, network.MaxNameLen)))
	case ipc.MethodSetNetworkRelays:
		var p ipc.SetRelaysParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		id, err := parseNetID(p.NetworkID)
		if err != nil {
			return nil, err
		}
		var rs []network.Relay
		for _, r := range p.Relays {
			if r.UDPPort <= 0 || r.UDPPort > 65535 || r.TCPPort < 0 || r.TCPPort > 65535 {
				return nil, ipc.Errorf(ipc.CodeInvalid, "invalid relay port")
			}
			rs = append(rs, network.Relay{Host: strings.TrimSpace(r.Host), UDPPort: uint16(r.UDPPort), TCPPort: uint16(r.TCPPort)})
		}
		return true, mapErr(e.SetNetworkRelays(id, rs))
	case ipc.MethodActivateNetwork, ipc.MethodLeaveNetwork:
		var p ipc.NetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		var id network.ID
		if p.NetworkID != "" {
			var err error
			if id, err = parseNetID(p.NetworkID); err != nil {
				return nil, err
			}
		}
		if method == ipc.MethodActivateNetwork {
			return true, mapErr(e.ActivateNetwork(id))
		}
		return true, mapErr(e.LeaveNetwork(id))
	case ipc.MethodListPeers:
		return e.Peers(), nil
	case ipc.MethodGetSettings:
		return e.Settings(), nil
	case ipc.MethodSetSettings:
		var p ipc.Settings
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		s, err := e.SetSettings(p)
		return s, mapErr(err)
	case ipc.MethodCreateConnCode:
		var p ipc.KeyParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		pk, err := parseKey(p.PublicKey)
		if err != nil {
			return nil, err
		}
		code, exp, err := e.CreateConnCode(pk)
		return ipc.ConnCodeResult{Code: code, Expires: exp}, mapErr(err)
	case ipc.MethodUseConnCode:
		var p ipc.CodeParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		pk, err := e.UseConnCode(p.Code)
		return ipc.KeyParams{PublicKey: pk.Hex()}, mapErr(err)
	case ipc.MethodCreateInvite:
		var p ipc.InviteParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		id, err := parseNetID(p.NetworkID)
		if err != nil {
			return nil, err
		}
		code, exp, err := e.CreateInvite(id, p.Reset)
		return ipc.InviteResult{Code: code, Expires: exp}, mapErr(err)
	case ipc.MethodRedeemInvite:
		var p ipc.CodeParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		res, err := e.RedeemInvite(p.Code)
		return res, mapErr(err)
	case ipc.MethodListInvites:
		return e.PendingJoins(), nil
	case ipc.MethodCancelInvite:
		var p ipc.NetworkParams
		if err := ipc.Decode(params, &p); err != nil {
			return nil, err
		}
		id, err := parseNetID(p.NetworkID)
		if err != nil {
			return nil, err
		}
		return true, mapErr(e.CancelJoin(id))
	case ipc.MethodExportDiagnostics:
		return ipc.DiagnosticsResult{Report: e.Diagnostics()}, nil
	}
	return nil, ipc.Errorf(ipc.CodeInvalid, "unknown method %q", method)
}
