package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

// ---- test doubles ----

type emitted struct {
	name string
	data any
}

type fakeHost struct {
	mu        sync.Mutex
	events    []emitted
	clipboard string
	clipErr   error
	savePath  string // returned by SaveFile; "" means the user cancelled
	openPath  string
	saveTitle string
	saveName  string
}

func (h *fakeHost) Emit(event string, data any) {
	h.mu.Lock()
	h.events = append(h.events, emitted{event, data})
	h.mu.Unlock()
}

func (h *fakeHost) SaveFile(title, defaultName string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saveTitle, h.saveName = title, defaultName
	return h.savePath, nil
}

func (h *fakeHost) OpenFile(title string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openPath, nil
}

func (h *fakeHost) SetClipboard(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clipboard = text
	return h.clipErr
}

// wait polls the recorded events until pred matches one, or fails.
func (h *fakeHost) wait(t *testing.T, name string, pred func(data any) bool) any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, e := range h.events {
			if e.name == name && (pred == nil || pred(e.data)) {
				h.mu.Unlock()
				return e.data
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event %q did not arrive; got %v", name, h.names())
	return nil
}

func (h *fakeHost) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e.name)
	}
	return out
}

func (h *fakeHost) clear() {
	h.mu.Lock()
	h.events = nil
	h.mu.Unlock()
}

func serviceIs(connected bool) func(any) bool {
	return func(d any) bool {
		s, ok := d.(ServiceState)
		return ok && s.Connected == connected
	}
}

type call struct {
	Method string
	Params json.RawMessage
}

type fakeService struct {
	addr   string
	srv    *ipc.Server
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	calls []call
	// reply maps a method to its result; an *ipc.Error value is returned as an error.
	reply map[string]any
}

func testAddr(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\lanfolk-ui-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	return filepath.Join(t.TempDir(), "l.sock")
}

func newFake(t *testing.T, addr string, reply map[string]any) *fakeService {
	t.Helper()
	f := &fakeService{addr: addr, reply: reply}
	f.start(t)
	t.Cleanup(f.stop)
	return f
}

func (f *fakeService) start(t *testing.T) {
	t.Helper()
	ln, err := ipc.Listen(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = &ipc.Server{Handler: f.handle}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() { f.srv.Serve(ctx, ln); close(f.done) }()
}

func (f *fakeService) stop() {
	if f.cancel != nil {
		f.cancel()
		<-f.done
		f.cancel = nil
	}
}

func (f *fakeService) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{method, append(json.RawMessage(nil), params...)})
	r, ok := f.reply[method]
	f.mu.Unlock()
	if !ok {
		return true, nil
	}
	if e, isErr := r.(*ipc.Error); isErr {
		return nil, e
	}
	return r, nil
}

// last returns the most recent call of a method. (The app also makes its own
// snapshot calls right after connecting, so "the last call" is not stable.)
func (f *fakeService) last(t *testing.T, method string) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i]
		}
	}
	t.Fatalf("service received no %q call", method)
	return call{}
}

func fastApp(addr string, host Host) *App {
	a := NewApp(addr, host)
	a.backoffMin = 5 * time.Millisecond
	a.backoffMax = 40 * time.Millisecond
	a.callTimeout = 3 * time.Second
	return a
}

func startedApp(t *testing.T, f *fakeService) (*App, *fakeHost) {
	t.Helper()
	h := &fakeHost{}
	a := fastApp(f.addr, h)
	a.start(context.Background())
	t.Cleanup(a.stop)
	h.wait(t, "service", serviceIs(true))
	return a, h
}

func jsonEq(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("params %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("params = %s, want %s", got, want)
	}
}

// ---- tests ----

func TestCallsAreForwarded(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodStatus:         ipc.Status{Version: "1.2.3", Nickname: "Ahmet"},
		ipc.MethodListFriends:    []ipc.Friend{{Nickname: "Veli", State: "active"}},
		ipc.MethodAddFriend:      ipc.Friend{Nickname: "Mehmet", State: "pending"},
		ipc.MethodListNetworks:   []ipc.Network{{ID: "n1", Name: "LAN"}},
		ipc.MethodCreateNetwork:  ipc.Network{ID: "n2", Name: "New"},
		ipc.MethodListPeers:      []ipc.Peer{{Nickname: "Veli", Path: "direct"}},
		ipc.MethodGetSettings:    ipc.Settings{ManualPort: 5000, DefaultRelay: "r:3478"},
		ipc.MethodExportIdentity: ipc.BackupResult{Backup: "BACKUP"},
		ipc.MethodCreateConnCode: ipc.ConnCodeResult{Code: "CODE", Expires: time.Unix(1700000000, 0).UTC()},
	})
	a, _ := startedApp(t, f)

	st, err := a.Status()
	if err != nil || st.Version != "1.2.3" {
		t.Fatalf("Status: %v %+v", err, st)
	}
	f.last(t, ipc.MethodStatus)
	if fr, err := a.ListFriends(); err != nil || len(fr) != 1 || fr[0].Nickname != "Veli" {
		t.Fatalf("ListFriends: %v %+v", err, fr)
	}
	if fr, err := a.AddFriend("lanfolk1abc", "Mehmet"); err != nil || fr == nil || fr.State != "pending" {
		t.Fatalf("AddFriend: %v %+v", err, fr)
	}
	jsonEq(t, f.last(t, ipc.MethodAddFriend).Params, `{"code":"lanfolk1abc","nickname":"Mehmet"}`)
	if nets, err := a.ListNetworks(); err != nil || len(nets) != 1 {
		t.Fatalf("ListNetworks: %v", err)
	}
	if n, err := a.CreateNetwork("New"); err != nil || n == nil || n.ID != "n2" {
		t.Fatalf("CreateNetwork: %v %+v", err, n)
	}
	if peers, err := a.ListPeers(); err != nil || len(peers) != 1 {
		t.Fatalf("ListPeers: %v", err)
	}
	if s, err := a.GetSettings(); err != nil || s.ManualPort != 5000 {
		t.Fatalf("GetSettings: %v %+v", err, s)
	}
	if b, err := a.ExportIdentity("pw"); err != nil || b != "BACKUP" {
		t.Fatalf("ExportIdentity: %v %q", err, b)
	}
	if cc, err := a.CreateConnCode("PUBKEY"); err != nil || cc.Code != "CODE" {
		t.Fatalf("CreateConnCode: %v %+v", err, cc)
	}

	relays := []ipc.Relay{{Host: "r.example", UDPPort: 3478, TCPPort: 443}}
	cases := []struct {
		name   string
		run    func() error
		method string
		params string
	}{
		{"RemoveFriend", func() error { return a.RemoveFriend("K1") }, ipc.MethodRemoveFriend, `{"public_key":"K1"}`},
		{"AddMember", func() error { return a.AddMember("n1", "K2") }, ipc.MethodAddMember, `{"network_id":"n1","public_key":"K2"}`},
		{"RemoveMember", func() error { return a.RemoveMember("n1", "K2") }, ipc.MethodRemoveMember, `{"network_id":"n1","public_key":"K2"}`},
		{"RenameNetwork", func() error { return a.RenameNetwork("n1", "Fresh") }, ipc.MethodRenameNetwork, `{"network_id":"n1","name":"Fresh"}`},
		{"SetNetworkRelays", func() error { return a.SetNetworkRelays("n1", relays) }, ipc.MethodSetNetworkRelays,
			`{"network_id":"n1","relays":[{"host":"r.example","udp_port":3478,"tcp_port":443}]}`},
		{"ActivateNetwork", func() error { return a.ActivateNetwork("n1") }, ipc.MethodActivateNetwork, `{"network_id":"n1"}`},
		{"LeaveNetwork", func() error { return a.LeaveNetwork("n1") }, ipc.MethodLeaveNetwork, `{"network_id":"n1"}`},
		{"SetSettings", func() error {
			return a.SetSettings(ipc.Settings{RelayOnly: true, ManualPort: 7777, CustomRelays: relays})
		},
			ipc.MethodSetSettings,
			`{"relay_only":true,"compat_mode":false,"manual_port":7777,"port_mapping":false,"dht_fallback":false,` +
				`"custom_relays":[{"host":"r.example","udp_port":3478,"tcp_port":443}],"default_relay":"","notifications":false,"check_updates":false}`},
		{"SetNickname", func() error { return a.SetNickname("Ahmet") }, ipc.MethodSetNickname, `{"nickname":"Ahmet"}`},
		{"ImportIdentity", func() error { return a.ImportIdentity("BACKUP", "pw") }, ipc.MethodImportIdentity, `{"backup":"BACKUP","password":"pw"}`},
		{"UseConnCode", func() error { return a.UseConnCode("CODE") }, ipc.MethodUseConnCode, `{"code":"CODE"}`},
	}
	for _, c := range cases {
		if err := c.run(); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		jsonEq(t, f.last(t, c.method).Params, c.params)
	}
	// The create-conn-code call carries the friend's key.
	f.mu.Lock()
	var cc call
	for _, c := range f.calls {
		if c.Method == ipc.MethodCreateConnCode {
			cc = c
		}
	}
	f.mu.Unlock()
	jsonEq(t, cc.Params, `{"public_key":"PUBKEY"}`)
}

func TestMutatingCallsTolerateUnexpectedResults(t *testing.T) {
	// The service may answer with anything for mutating calls; the UI must not fail on it.
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodAddFriend:     "ok",
		ipc.MethodCreateNetwork: true,
	})
	a, _ := startedApp(t, f)
	if fr, err := a.AddFriend("c", ""); err != nil || fr != nil {
		t.Fatalf("AddFriend: %v %v", err, fr)
	}
	if n, err := a.CreateNetwork("x"); err != nil || n != nil {
		t.Fatalf("CreateNetwork: %v %v", err, n)
	}
}

func TestNilListsBecomeEmpty(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodStatus:       ipc.Status{},
		ipc.MethodListFriends:  nil,
		ipc.MethodListNetworks: nil,
		ipc.MethodListPeers:    nil,
	})
	a, _ := startedApp(t, f)
	if fr, err := a.ListFriends(); err != nil || fr == nil || len(fr) != 0 {
		t.Fatalf("friends %v %v", fr, err)
	}
	if n, err := a.ListNetworks(); err != nil || n == nil {
		t.Fatalf("networks %v %v", n, err)
	}
	if p, err := a.ListPeers(); err != nil || p == nil {
		t.Fatalf("peers %v %v", p, err)
	}
	st, err := a.Status()
	if err != nil || st.Relays == nil || st.Warnings == nil {
		t.Fatalf("status slices must not be nil: %+v %v", st, err)
	}
}

func TestErrorsAreMappedToReadableMessages(t *testing.T) {
	codes := []string{ipc.CodeInvalid, ipc.CodeNotFound, ipc.CodeForbidden, ipc.CodeUnauthorized, ipc.CodeInternal, ipc.CodeUnavailable}
	reply := map[string]any{}
	f := newFake(t, testAddr(t), reply)
	a, _ := startedApp(t, f)
	for _, code := range codes {
		f.mu.Lock()
		f.reply[ipc.MethodRemoveFriend] = ipc.Errorf(code, "service says %s", code)
		f.mu.Unlock()
		err := a.RemoveFriend("K")
		var ue *UserError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: got %T %v", code, err, err)
		}
		if ue.Code != code {
			t.Errorf("code = %q, want %q", ue.Code, code)
		}
		if ue.Message == "" || ue.Error() != ue.Message {
			t.Errorf("%s: empty message", code)
		}
		if code != ipc.CodeUnauthorized && !strings.Contains(strings.ToLower(ue.Message), "service says "+code) {
			t.Errorf("%s: service text lost: %q", code, ue.Message)
		}
		if strings.Contains(ue.Message, "{") || strings.Contains(ue.Message, "ipc.") {
			t.Errorf("%s: message is not readable: %q", code, ue.Message)
		}
	}
	// Internal errors must not read like user mistakes.
	if m := userMessage(ipc.Errorf(ipc.CodeInternal, "boom")); !strings.Contains(strings.ToLower(m), "internal") {
		t.Errorf("internal message %q", m)
	}
}

func TestNotConnected(t *testing.T) {
	h := &fakeHost{}
	a := fastApp(testAddr(t), h) // nothing listens, and the run loop is not started
	_, err := a.Status()
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != CodeNotRunning {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(ue.Message, "not running") {
		t.Fatalf("message %q", ue.Message)
	}
	if a.Connected() {
		t.Fatal("Connected() true without a service")
	}
}

func TestStartsBeforeService(t *testing.T) {
	addr := testAddr(t)
	h := &fakeHost{}
	a := fastApp(addr, h)
	a.start(context.Background())
	defer a.stop()
	h.wait(t, "service", serviceIs(false))
	if a.Connected() {
		t.Fatal("connected without a service")
	}
	newFake(t, addr, map[string]any{ipc.MethodStatus: ipc.Status{Version: "late"}})
	h.wait(t, "service", serviceIs(true))
	if !a.Connected() {
		t.Fatal("Connected() false after the service appeared")
	}
	if st, err := a.Status(); err != nil || st.Version != "late" {
		t.Fatalf("%v %+v", err, st)
	}
}

func TestReconnectsAfterServiceRestart(t *testing.T) {
	addr := testAddr(t)
	f := newFake(t, addr, map[string]any{ipc.MethodStatus: ipc.Status{Version: "one"}})
	a, h := startedApp(t, f)

	f.stop()
	h.wait(t, "service", serviceIs(false))
	if _, err := a.Status(); err == nil {
		t.Fatal("call succeeded with the service down")
	}
	if a.Connected() {
		t.Fatal("Connected() true after the service stopped")
	}

	h.clear()
	f2 := &fakeService{addr: addr, reply: map[string]any{ipc.MethodStatus: ipc.Status{Version: "two"}}}
	f2.start(t)
	t.Cleanup(f2.stop)
	h.wait(t, "service", serviceIs(true))
	st, err := a.Status()
	if err != nil || st.Version != "two" {
		t.Fatalf("after restart: %v %+v", err, st)
	}
	// Events work on the new connection.
	f2.srv.Publish(ipc.EventFriends, []ipc.Friend{{Nickname: "Veli"}})
	h.wait(t, "friends", func(d any) bool { fr, ok := d.([]ipc.Friend); return ok && len(fr) == 1 && fr[0].Nickname == "Veli" })
}

func TestEventsAreReemitted(t *testing.T) {
	f := newFake(t, testAddr(t), nil)
	_, h := startedApp(t, f)
	h.clear()

	f.srv.Publish(ipc.EventStatus, ipc.Status{Version: "9", ActiveNetworkName: "Friday"})
	f.srv.Publish(ipc.EventPeers, []ipc.Peer{{Nickname: "Veli", Path: "direct", PingMs: 12.5, Quality: "good"}})
	f.srv.Publish(ipc.EventFriends, []ipc.Friend{{Nickname: "Mehmet", State: "pending"}})
	f.srv.Publish(ipc.EventNetworks, []ipc.Network{{ID: "n1", Name: "Friday", Active: true}})

	if d := h.wait(t, "status", func(d any) bool { s, ok := d.(ipc.Status); return ok && s.Version == "9" }); d.(ipc.Status).ActiveNetworkName != "Friday" {
		t.Errorf("status %+v", d)
	}
	h.wait(t, "peers", func(d any) bool {
		p, ok := d.([]ipc.Peer)
		return ok && len(p) == 1 && p[0].PingMs == 12.5 && p[0].Quality == "good"
	})
	h.wait(t, "friends", func(d any) bool { fr, ok := d.([]ipc.Friend); return ok && fr[0].State == "pending" })
	h.wait(t, "networks", func(d any) bool { n, ok := d.([]ipc.Network); return ok && n[0].Active })

	// An unknown event or garbage data must not break the stream.
	f.srv.Publish("mystery", 1)
	f.srv.Publish(ipc.EventPeers, "not a list")
	f.srv.Publish(ipc.EventPeers, []ipc.Peer{{Nickname: "Last"}})
	h.wait(t, "peers", func(d any) bool { p, ok := d.([]ipc.Peer); return ok && p[0].Nickname == "Last" })
}

func TestSnapshotOnConnect(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{
		ipc.MethodStatus:       ipc.Status{Version: "snap"},
		ipc.MethodListFriends:  []ipc.Friend{{Nickname: "F"}},
		ipc.MethodListNetworks: []ipc.Network{{ID: "n"}},
		ipc.MethodListPeers:    []ipc.Peer{{Nickname: "P"}},
	})
	_, h := startedApp(t, f)
	for _, name := range []string{"status", "friends", "networks", "peers"} {
		h.wait(t, name, nil)
	}
}

func TestExportDiagnosticsSavesReport(t *testing.T) {
	f := newFake(t, testAddr(t), map[string]any{ipc.MethodExportDiagnostics: ipc.DiagnosticsResult{Report: "REPORT BODY"}})
	a, h := startedApp(t, f)

	// Cancelled dialog: nothing is written.
	if p, err := a.ExportDiagnostics(); err != nil || p != "" {
		t.Fatalf("cancelled export: %q %v", p, err)
	}
	out := filepath.Join(t.TempDir(), "report.txt")
	h.mu.Lock()
	h.savePath = out
	h.mu.Unlock()
	p, err := a.ExportDiagnostics()
	if err != nil || p != out {
		t.Fatalf("export: %q %v", p, err)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "REPORT BODY" {
		t.Fatalf("file: %q %v", b, err)
	}
	if !strings.HasSuffix(h.saveName, ".txt") || !strings.Contains(h.saveName, "lanfolk") {
		t.Errorf("default file name %q", h.saveName)
	}
}

func TestSaveAndOpenTextFile(t *testing.T) {
	h := &fakeHost{}
	a := fastApp(testAddr(t), h)
	dir := t.TempDir()
	h.savePath = filepath.Join(dir, "backup.txt")
	if p, err := a.SaveTextFile("Save backup", "backup.txt", "hello"); err != nil || p != h.savePath {
		t.Fatalf("%q %v", p, err)
	}
	h.openPath = h.savePath
	if s, err := a.OpenTextFile("Open backup"); err != nil || s != "hello" {
		t.Fatalf("%q %v", s, err)
	}
	h.openPath = ""
	if s, err := a.OpenTextFile("Open backup"); err != nil || s != "" {
		t.Fatalf("cancelled open: %q %v", s, err)
	}
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, make([]byte, maxTextFile+1), 0o600)
	h.openPath = big
	if _, err := a.OpenTextFile("Open backup"); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestCopyToClipboard(t *testing.T) {
	h := &fakeHost{}
	a := fastApp(testAddr(t), h)
	if err := a.CopyToClipboard("lanfolk1code"); err != nil || h.clipboard != "lanfolk1code" {
		t.Fatalf("%v %q", err, h.clipboard)
	}
	h.clipErr = errors.New("denied")
	if err := a.CopyToClipboard("x"); err == nil {
		t.Fatal("clipboard error swallowed")
	}
}

func TestRetryReconnectsWithoutWaitingForBackoff(t *testing.T) {
	addr := testAddr(t)
	h := &fakeHost{}
	a := fastApp(addr, h)
	a.backoffMin, a.backoffMax = time.Hour, time.Hour // only Retry can get us connected in time
	a.start(context.Background())
	defer a.stop()
	h.wait(t, "service", serviceIs(false))
	newFake(t, addr, nil)
	a.Retry()
	h.wait(t, "service", serviceIs(true))
}

func TestStopEndsRunLoop(t *testing.T) {
	f := newFake(t, testAddr(t), nil)
	h := &fakeHost{}
	a := fastApp(f.addr, h)
	a.start(context.Background())
	h.wait(t, "service", serviceIs(true))
	done := make(chan struct{})
	go func() { a.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not return")
	}
	if a.Connected() {
		t.Fatal("still connected after stop")
	}
}
