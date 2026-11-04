package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// Events the frontend can listen to with window.runtime.EventsOn.
const (
	eventService = "service" // ServiceState
	// "status", "peers", "friends" and "networks" use the names in package ipc.
)

// ServiceState is the payload of the "service" event.
type ServiceState struct {
	Connected bool `json:"connected"`
	// Message explains why the service cannot be reached, when it is known.
	Message string `json:"message,omitempty"`
}

const (
	defaultCallTimeout = 15 * time.Second
	slowCallTimeout    = 60 * time.Second // password hashing, diagnostics
	dialTimeout        = 3 * time.Second
	maxTextFile        = 1 << 20
)

// App is bound to the frontend: every exported method can be called from
// JavaScript as window.go.main.App.<Method>(). The service owns all state;
// App holds one connection to it and forwards calls.
//
// Methods that fail return an error with a readable message (see UserError).
type App struct {
	addr string
	host Host

	// Tunables, changed by tests.
	backoffMin  time.Duration
	backoffMax  time.Duration
	callTimeout time.Duration

	mu        sync.RWMutex
	client    *ipc.Client
	announced bool // a "service" event has been sent
	lastState ServiceState

	wake chan struct{} // Retry wakes the connection loop

	cancel context.CancelFunc
	done   chan struct{}
}

// NewApp creates an App that talks to the service at addr.
func NewApp(addr string, host Host) *App {
	return &App{
		addr:        addr,
		host:        host,
		backoffMin:  500 * time.Millisecond,
		backoffMax:  5 * time.Second,
		callTimeout: defaultCallTimeout,
		wake:        make(chan struct{}, 1),
	}
}

// start begins connecting to the service in the background.
func (a *App) start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		a.run(ctx)
	}()
}

// stop ends the connection loop and closes the connection.
func (a *App) stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	<-a.done
	a.cancel = nil
}

// run keeps one connection to the service alive: it dials with backoff,
// subscribes, re-emits events and starts over when the connection ends.
func (a *App) run(ctx context.Context) {
	delay := a.backoffMin
	for ctx.Err() == nil {
		c, err := a.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.publishState(ServiceState{Connected: false, Message: serviceMessage(err)})
			if !a.sleep(ctx, delay) {
				return
			}
			delay = min(delay*2, a.backoffMax)
			continue
		}
		delay = a.backoffMin
		a.mu.Lock()
		a.client = c
		a.mu.Unlock()
		a.publishState(ServiceState{Connected: true})
		a.snapshot(ctx, c)
		a.pump(ctx, c)
		c.Close()
		a.mu.Lock()
		a.client = nil
		a.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		a.publishState(ServiceState{Connected: false, Message: "The connection to the Lanfolk service was lost."})
		// Retry soon: the service may just be restarting.
		if !a.sleep(ctx, a.backoffMin) {
			return
		}
	}
}

func serviceMessage(err error) string {
	if ue, ok := userError(err).(*UserError); ok && ue.Code != CodeNotRunning {
		return ue.Message
	}
	return ""
}

// sleep waits for d, or until Retry is called. It returns false when ctx ends.
func (a *App) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-a.wake:
		return true
	case <-t.C:
		return true
	}
}

// Retry makes the connection loop try again right away instead of waiting
// for its backoff delay. It does not block.
func (a *App) Retry() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Resync sends the service state and, when connected, a fresh snapshot of
// status, networks, peers, friends and invites. The page calls it once its
// listeners are registered: events emitted before the page loaded are lost
// (OnStartup connects before the frontend's JavaScript runs).
func (a *App) Resync() {
	a.mu.RLock()
	c := a.client
	s := a.lastState
	known := a.announced
	a.mu.RUnlock()
	if !known {
		// The connection loop has not decided yet; its first event follows.
		return
	}
	a.host.Emit(eventService, s)
	if c != nil {
		go a.snapshot(context.Background(), c)
	}
}

func (a *App) connect(ctx context.Context) (*ipc.Client, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, err := ipc.DialAddr(dctx, a.addr)
	if err != nil {
		return nil, err
	}
	if err := c.Subscribe(dctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// publishState emits a "service" event when the state changed (and always
// the first time, so the frontend learns the initial state).
func (a *App) publishState(s ServiceState) {
	a.mu.Lock()
	changed := !a.announced || a.lastState != s
	a.announced = true
	a.lastState = s
	a.mu.Unlock()
	if changed {
		a.host.Emit(eventService, s)
	}
}

// snapshot sends the current state to the frontend right after connecting,
// so it does not have to wait for the next change.
func (a *App) snapshot(ctx context.Context, c *ipc.Client) {
	cctx, cancel := context.WithTimeout(ctx, a.callTimeout)
	defer cancel()
	var st ipc.Status
	if c.Call(cctx, ipc.MethodStatus, nil, &st) == nil {
		a.host.Emit(ipc.EventStatus, normalizeStatus(st))
	}
	var fr []ipc.Friend
	if c.Call(cctx, ipc.MethodListFriends, nil, &fr) == nil {
		a.host.Emit(ipc.EventFriends, orEmpty(fr))
	}
	var nets []ipc.Network
	if c.Call(cctx, ipc.MethodListNetworks, nil, &nets) == nil {
		a.host.Emit(ipc.EventNetworks, normalizeNetworks(nets))
	}
	var peers []ipc.Peer
	if c.Call(cctx, ipc.MethodListPeers, nil, &peers) == nil {
		a.host.Emit(ipc.EventPeers, orEmpty(peers))
	}
	var joins []ipc.PendingJoin
	if c.Call(cctx, ipc.MethodListInvites, nil, &joins) == nil {
		a.host.Emit(ipc.EventInvites, orEmpty(joins))
	}
}

// pump forwards pushed events until the connection ends.
func (a *App) pump(ctx context.Context, c *ipc.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-c.Events():
			if !ok {
				return
			}
			a.forward(m)
		}
	}
}

func (a *App) forward(m *ipc.Message) {
	switch m.Event {
	case ipc.EventStatus:
		var v ipc.Status
		if json.Unmarshal(m.Data, &v) == nil {
			a.host.Emit(m.Event, normalizeStatus(v))
		}
	case ipc.EventPeers:
		var v []ipc.Peer
		if json.Unmarshal(m.Data, &v) == nil {
			a.host.Emit(m.Event, orEmpty(v))
		}
	case ipc.EventFriends:
		var v []ipc.Friend
		if json.Unmarshal(m.Data, &v) == nil {
			a.host.Emit(m.Event, orEmpty(v))
		}
	case ipc.EventNetworks:
		var v []ipc.Network
		if json.Unmarshal(m.Data, &v) == nil {
			a.host.Emit(m.Event, normalizeNetworks(v))
		}
	case ipc.EventInvites:
		var v []ipc.PendingJoin
		if json.Unmarshal(m.Data, &v) == nil {
			a.host.Emit(m.Event, orEmpty(v))
		}
	}
}

// orEmpty makes a nil slice encode as [] instead of null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func normalizeStatus(s ipc.Status) ipc.Status {
	s.Relays = orEmpty(s.Relays)
	s.Warnings = orEmpty(s.Warnings)
	return s
}

func normalizeNetworks(n []ipc.Network) []ipc.Network {
	n = orEmpty(n)
	for i := range n {
		n[i].Members = orEmpty(n[i].Members)
		n[i].Relays = orEmpty(n[i].Relays)
	}
	return n
}

// call performs one request on the current connection.
func (a *App) call(method string, params, out any) error {
	return a.callWithTimeout(a.callTimeout, method, params, out)
}

func (a *App) callWithTimeout(d time.Duration, method string, params, out any) error {
	a.mu.RLock()
	c := a.client
	a.mu.RUnlock()
	if c == nil {
		return errNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return userError(c.Call(ctx, method, params, out))
}

// callLoose calls a method whose result is optional: the call succeeds even
// when the service answers with something the UI does not understand.
func callLoose[T any](a *App, method string, params any) (*T, error) {
	var raw json.RawMessage
	if err := a.call(method, params, &raw); err != nil {
		return nil, err
	}
	var v T
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil, nil
	}
	return &v, nil
}

// Connected reports whether the service is reachable right now. The
// frontend also gets "service" events.
func (a *App) Connected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client != nil
}

// ---- status and peers ----

// Status returns the service status.
func (a *App) Status() (ipc.Status, error) {
	var st ipc.Status
	if err := a.call(ipc.MethodStatus, nil, &st); err != nil {
		return ipc.Status{}, err
	}
	return normalizeStatus(st), nil
}

// ListPeers returns the live connection state of the active network's members.
func (a *App) ListPeers() ([]ipc.Peer, error) {
	var p []ipc.Peer
	if err := a.call(ipc.MethodListPeers, nil, &p); err != nil {
		return nil, err
	}
	return orEmpty(p), nil
}

// ---- identity ----

// SetNickname changes the display nickname.
func (a *App) SetNickname(nickname string) error {
	return a.call(ipc.MethodSetNickname, ipc.NicknameParams{Nickname: nickname}, nil)
}

// ExportIdentity returns a password-protected backup of the identity.
func (a *App) ExportIdentity(password string) (string, error) {
	var res ipc.BackupResult
	if err := a.callWithTimeout(slowCallTimeout, ipc.MethodExportIdentity, ipc.PasswordParams{Password: password}, &res); err != nil {
		return "", err
	}
	return res.Backup, nil
}

// ImportIdentity replaces the current identity with a backup.
func (a *App) ImportIdentity(backup, password string) error {
	return a.callWithTimeout(slowCallTimeout, ipc.MethodImportIdentity, ipc.ImportParams{Backup: backup, Password: password}, nil)
}

// ---- friends ----

// ListFriends returns the friend list.
func (a *App) ListFriends() ([]ipc.Friend, error) {
	var f []ipc.Friend
	if err := a.call(ipc.MethodListFriends, nil, &f); err != nil {
		return nil, err
	}
	return orEmpty(f), nil
}

// AddFriend adds a friend by friend code. The friendship becomes active when
// the friend has added this user's code too.
func (a *App) AddFriend(code, nickname string) (*ipc.Friend, error) {
	return callLoose[ipc.Friend](a, ipc.MethodAddFriend, ipc.AddFriendParams{Code: code, Nickname: nickname})
}

// RemoveFriend removes a friend by public key.
func (a *App) RemoveFriend(publicKey string) error {
	return a.call(ipc.MethodRemoveFriend, ipc.KeyParams{PublicKey: publicKey}, nil)
}

// CreateConnCode creates a time-limited connection code for a friend.
func (a *App) CreateConnCode(publicKey string) (ipc.ConnCodeResult, error) {
	var res ipc.ConnCodeResult
	if err := a.call(ipc.MethodCreateConnCode, ipc.KeyParams{PublicKey: publicKey}, &res); err != nil {
		return ipc.ConnCodeResult{}, err
	}
	return res, nil
}

// UseConnCode applies a connection code received from a friend.
func (a *App) UseConnCode(code string) error {
	return a.call(ipc.MethodUseConnCode, ipc.CodeParams{Code: code}, nil)
}

// ---- networks ----

// ListNetworks returns the networks this user belongs to.
func (a *App) ListNetworks() ([]ipc.Network, error) {
	var n []ipc.Network
	if err := a.call(ipc.MethodListNetworks, nil, &n); err != nil {
		return nil, err
	}
	return normalizeNetworks(n), nil
}

// CreateNetwork creates a network owned by this user.
func (a *App) CreateNetwork(name string) (*ipc.Network, error) {
	return callLoose[ipc.Network](a, ipc.MethodCreateNetwork, ipc.CreateNetworkParams{Name: name})
}

// AddMember adds an active friend to an owned network.
func (a *App) AddMember(networkID, publicKey string) error {
	return a.call(ipc.MethodAddMember, ipc.MemberParams{NetworkID: networkID, PublicKey: publicKey}, nil)
}

// RemoveMember removes a member from an owned network.
func (a *App) RemoveMember(networkID, publicKey string) error {
	return a.call(ipc.MethodRemoveMember, ipc.MemberParams{NetworkID: networkID, PublicKey: publicKey}, nil)
}

// RenameNetwork renames an owned network.
func (a *App) RenameNetwork(networkID, name string) error {
	return a.call(ipc.MethodRenameNetwork, ipc.RenameParams{NetworkID: networkID, Name: name}, nil)
}

// SetNetworkRelays sets the owner's relays (at most three) for a network.
func (a *App) SetNetworkRelays(networkID string, relays []ipc.Relay) error {
	return a.call(ipc.MethodSetNetworkRelays, ipc.SetRelaysParams{NetworkID: networkID, Relays: orEmpty(relays)}, nil)
}

// ActivateNetwork makes a network the active one. An empty networkID
// deactivates the current network (the user is active in one network at a
// time).
func (a *App) ActivateNetwork(networkID string) error {
	return a.call(ipc.MethodActivateNetwork, ipc.NetworkParams{NetworkID: networkID}, nil)
}

// LeaveNetwork leaves a network.
func (a *App) LeaveNetwork(networkID string) error {
	return a.call(ipc.MethodLeaveNetwork, ipc.NetworkParams{NetworkID: networkID}, nil)
}

// ---- invites ----

// CreateInvite returns the invite code of an owned network. reset replaces
// the current code with a new one, so the old code stops working.
func (a *App) CreateInvite(networkID string, reset bool) (ipc.InviteResult, error) {
	var res ipc.InviteResult
	if err := a.call(ipc.MethodCreateInvite, ipc.InviteParams{NetworkID: networkID, Reset: reset}, &res); err != nil {
		return ipc.InviteResult{}, err
	}
	return res, nil
}

// RedeemInvite joins a network with an invite code a friend sent.
func (a *App) RedeemInvite(code string) (ipc.RedeemResult, error) {
	var res ipc.RedeemResult
	if err := a.call(ipc.MethodRedeemInvite, ipc.CodeParams{Code: strings.TrimSpace(code)}, &res); err != nil {
		return ipc.RedeemResult{}, err
	}
	return res, nil
}

// ListInvites returns the invites this user redeemed and their progress.
func (a *App) ListInvites() ([]ipc.PendingJoin, error) {
	var res []ipc.PendingJoin
	if err := a.call(ipc.MethodListInvites, nil, &res); err != nil {
		return nil, err
	}
	return orEmpty(res), nil
}

// CancelInvite stops waiting for a join.
func (a *App) CancelInvite(networkID string) error {
	return a.call(ipc.MethodCancelInvite, ipc.NetworkParams{NetworkID: networkID}, nil)
}

// ---- settings and diagnostics ----

// GetSettings returns the user-adjustable settings.
func (a *App) GetSettings() (ipc.Settings, error) {
	var s ipc.Settings
	if err := a.call(ipc.MethodGetSettings, nil, &s); err != nil {
		return ipc.Settings{}, err
	}
	s.CustomRelays = orEmpty(s.CustomRelays)
	return s, nil
}

// SetSettings stores the settings. DefaultRelay is read-only and ignored by
// the service.
func (a *App) SetSettings(s ipc.Settings) error {
	s.CustomRelays = orEmpty(s.CustomRelays)
	return a.call(ipc.MethodSetSettings, s, nil)
}

// ExportDiagnostics asks the service for a diagnostics report and saves it
// where the user chooses. It returns the saved path, or "" when the user
// cancelled the dialog.
func (a *App) ExportDiagnostics() (string, error) {
	var res ipc.DiagnosticsResult
	if err := a.callWithTimeout(slowCallTimeout, ipc.MethodExportDiagnostics, nil, &res); err != nil {
		return "", err
	}
	name := "lanfolk-diagnostics-" + time.Now().Format("2006-01-02") + ".txt"
	return a.SaveTextFile("Save diagnostics report", name, res.Report)
}

// ---- local helpers ----

// CopyToClipboard puts text on the clipboard.
func (a *App) CopyToClipboard(text string) error {
	if err := a.host.SetClipboard(text); err != nil {
		return localError("Could not copy to the clipboard", err)
	}
	return nil
}

// SaveTextFile asks the user where to save text and writes it there. It
// returns the path, or "" when the user cancelled.
func (a *App) SaveTextFile(title, defaultName, content string) (string, error) {
	path, err := a.host.SaveFile(title, defaultName)
	if err != nil {
		return "", localError("Could not open the save dialog", err)
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", localError("Could not save the file", err)
	}
	return path, nil
}

// OpenTextFile asks the user to pick a text file and returns its content, or
// "" when the user cancelled.
func (a *App) OpenTextFile(title string) (string, error) {
	path, err := a.host.OpenFile(title)
	if err != nil {
		return "", localError("Could not open the file dialog", err)
	}
	if path == "" {
		return "", nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", localError("Could not read the file", err)
	}
	if fi.Size() > maxTextFile {
		return "", &UserError{Code: CodeLocal, Message: "That file is too large to be a Lanfolk backup."}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", localError("Could not read the file", err)
	}
	return string(b), nil
}

func localError(what string, err error) error {
	return &UserError{Code: CodeLocal, Message: fmt.Sprintf("%s: %v", what, err)}
}

// ---- desktop integration ----

// StartWithWindows reports whether Lanfolk opens (in the tray) at sign-in.
func (a *App) StartWithWindows() (bool, error) {
	on, err := startWithWindows()
	if err != nil {
		return false, localError("Could not read the startup setting", err)
	}
	return on, nil
}

// SetStartWithWindows turns opening at sign-in on or off.
func (a *App) SetStartWithWindows(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return localError("Could not find Lanfolk.exe", err)
	}
	if err := setStartWithWindows(on, exe); err != nil {
		return localError("Could not change the startup setting", err)
	}
	return nil
}
