//go:build windows

package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

//go:embed assets/tray-*.ico
var trayIcons embed.FS

// maxTrayNetworks is how many networks the tray's Network submenu lists.
const maxTrayNetworks = 8

// tray is the notification-area icon and its menu. It follows the same
// events as the window (through teeHost) and calls the same App methods.
type tray struct {
	app  *App
	host *wailsHost

	mu      sync.Mutex
	snap    traySnapshot
	model   trayModel
	ready   bool
	pending bool
	dirty   chan struct{}

	status, copyIP, open, network, disconnect, invite, settings, quit *systray.MenuItem
	slots                                                             [maxTrayNetworks]*systray.MenuItem
	slotIDs                                                           [maxTrayNetworks]string
}

func newTray(app *App, host *wailsHost) *tray {
	return &tray{app: app, host: host, dirty: make(chan struct{}, 1)}
}

// start runs the tray on its own OS thread: the tray window and its message
// loop must live on one thread, separate from the Wails window's thread.
func (t *tray) start() {
	go func() {
		runtime.LockOSThread()
		allowDarkMenus()
		systray.Run(t.onReady, nil)
	}()
}

func (t *tray) stop() { systray.Quit() }

// observe receives every event the App emits to the window.
func (t *tray) observe(event string, data any) {
	t.mu.Lock()
	switch event {
	case eventService:
		if s, ok := data.(ServiceState); ok {
			t.snap.Connected = s.Connected
			if !s.Connected {
				t.snap.Status, t.snap.Peers = nil, nil
			}
		}
	case ipc.EventStatus:
		if s, ok := data.(ipc.Status); ok {
			t.snap.Status = &s
		}
	case ipc.EventNetworks:
		if n, ok := data.([]ipc.Network); ok {
			t.snap.Networks = n
		}
	case ipc.EventPeers:
		if p, ok := data.([]ipc.Peer); ok {
			t.snap.Peers = p
		}
	default:
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	select {
	case t.dirty <- struct{}{}:
	default:
	}
}

func (t *tray) onReady() {
	systray.SetIcon(t.icon(trayAttention))
	systray.SetTooltip("Lanfolk")
	t.status = systray.AddMenuItem("Lanfolk", "")
	t.status.Disable()
	t.copyIP = systray.AddMenuItem("Copy my IP address", "Copy your address in the active network")
	systray.AddSeparator()
	t.open = systray.AddMenuItem("Open Lanfolk", "")
	t.network = systray.AddMenuItem("Network", "Choose the active network")
	for i := range t.slots {
		t.slots[i] = t.network.AddSubMenuItemCheckbox("", "", false)
		t.slots[i].Hide()
	}
	t.network.AddSeparator()
	t.disconnect = t.network.AddSubMenuItem("Disconnect", "Leave the active network for now; you stay a member")
	t.invite = systray.AddMenuItem("Invite friends…", "Copy the invite code of the active network")
	systray.AddSeparator()
	t.settings = systray.AddMenuItem("Settings", "")
	t.quit = systray.AddMenuItem("Quit Lanfolk", "Close the app; the service keeps your network running")

	systray.SetOnTapped(t.toggleWindow)
	systray.SetOnSecondaryTapped(nil) // right click shows the menu (default)

	go t.handleClicks()
	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()
	go t.refreshLoop()
}

func (t *tray) icon(name string) []byte {
	b, err := trayIcons.ReadFile("assets/tray-" + name + ".ico")
	if err != nil {
		return nil
	}
	return b
}

// refreshLoop applies the newest model at most every 250 ms: peers events
// arrive every second and redrawing the icon each time is wasteful.
func (t *tray) refreshLoop() {
	t.apply(true)
	for range t.dirty {
		t.apply(false)
		time.Sleep(250 * time.Millisecond)
	}
}

func (t *tray) apply(force bool) {
	t.mu.Lock()
	m := trayView(t.snap)
	old := t.model
	t.model = m
	t.mu.Unlock()

	if force || m.Icon != old.Icon {
		systray.SetIcon(t.icon(m.Icon))
	}
	if force || m.Tooltip != old.Tooltip {
		systray.SetTooltip(m.Tooltip)
		t.status.SetTitle(m.Status)
	}
	if m.IP != "" {
		t.copyIP.SetTitle("Copy my IP address  (" + m.IP + ")")
		t.copyIP.Show()
	} else {
		t.copyIP.Hide()
	}
	if len(m.Networks) == 0 {
		t.network.Hide()
	} else {
		t.network.Show()
	}
	for i, s := range t.slots {
		if i >= len(m.Networks) {
			s.Hide()
			t.setSlot(i, "")
			continue
		}
		n := m.Networks[i]
		t.setSlot(i, n.ID)
		s.SetTitle(n.Name)
		if n.Active {
			s.Check()
		} else {
			s.Uncheck()
		}
		s.Show()
	}
	if m.ActiveNetworkID != "" {
		t.disconnect.Enable()
	} else {
		t.disconnect.Disable()
	}
	if m.CanInvite {
		t.invite.Show()
	} else {
		t.invite.Hide()
	}
}

func (t *tray) setSlot(i int, id string) {
	t.mu.Lock()
	t.slotIDs[i] = id
	t.mu.Unlock()
}

func (t *tray) handleClicks() {
	slotClicks := make(chan int)
	for i, s := range t.slots {
		go func(i int, ch <-chan struct{}) {
			for range ch {
				slotClicks <- i
			}
		}(i, s.ClickedCh)
	}
	for {
		select {
		case <-t.open.ClickedCh:
			t.doOpen()
		case <-t.copyIP.ClickedCh:
			t.doCopyIP()
		case i := <-slotClicks:
			t.mu.Lock()
			id := t.slotIDs[i]
			t.mu.Unlock()
			t.doNetwork(id)
			t.mu.Lock()
			active := id == t.model.ActiveNetworkID
			t.mu.Unlock()
			if active {
				t.slots[i].Check() // a click toggles the checkmark; keep it
			}
		case <-t.disconnect.ClickedCh:
			t.doDisconnect()
		case <-t.invite.ClickedCh:
			t.doInvite()
		case <-t.settings.ClickedCh:
			t.doSettings()
		case <-t.quit.ClickedCh:
			if ctx := t.host.context(); ctx != nil {
				wailsruntime.Quit(ctx)
			}
			return
		}
	}
}

// The tray actions; the menu and the automation API both call these.

func (t *tray) doOpen() { t.showWindow() }

func (t *tray) doCopyIP() {
	t.mu.Lock()
	ip := t.model.IP
	t.mu.Unlock()
	if ip != "" {
		t.app.CopyToClipboard(ip)
	}
}

func (t *tray) doNetwork(id string) {
	t.mu.Lock()
	active := id == t.model.ActiveNetworkID
	t.mu.Unlock()
	if id != "" && !active {
		go t.app.ActivateNetwork(id)
	}
}

func (t *tray) doDisconnect() { go t.app.ActivateNetwork("") }

func (t *tray) doInvite() {
	t.mu.Lock()
	id := t.model.ActiveNetworkID
	t.mu.Unlock()
	t.showWindow()
	t.navigate(map[string]string{"view": "invite", "network_id": id})
}

func (t *tray) doSettings() {
	t.showWindow()
	t.navigate(map[string]string{"view": "settings"})
}

// currentModel returns what the tray shows now.
func (t *tray) currentModel() trayModel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.model
}

// simulate runs a tray menu item as if it was clicked.
func (t *tray) simulate(item, id string) error {
	switch item {
	case "open":
		t.doOpen()
	case "copy-ip":
		t.doCopyIP()
	case "network":
		t.doNetwork(id)
	case "disconnect":
		t.doDisconnect()
	case "invite":
		t.doInvite()
	case "settings":
		t.doSettings()
	case "toggle":
		t.toggleWindow()
	default:
		return fmt.Errorf("unknown tray item %q", item)
	}
	return nil
}

// navigate asks the frontend to open a view (handled in app.js).
func (t *tray) navigate(v map[string]string) {
	b, _ := json.Marshal(v)
	t.host.Emit("navigate", json.RawMessage(b))
}

func (t *tray) showWindow() {
	ctx := t.host.context()
	if ctx == nil {
		return
	}
	showWindow(ctx)
}

func (t *tray) toggleWindow() {
	ctx := t.host.context()
	if ctx == nil {
		return
	}
	if windowVisible() {
		wailsruntime.WindowHide(ctx)
		return
	}
	showWindow(ctx)
}

func showWindow(ctx context.Context) {
	wailsruntime.WindowShow(ctx)
	wailsruntime.WindowUnminimise(ctx)
	// WindowShow does not take the focus from another app; a short
	// always-on-top flip brings the window to the front reliably.
	wailsruntime.WindowSetAlwaysOnTop(ctx, true)
	wailsruntime.WindowSetAlwaysOnTop(ctx, false)
}

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procFindWindow  = user32.NewProc("FindWindowW")
	procIsWindowVis = user32.NewProc("IsWindowVisible")
	procIsIconic    = user32.NewProc("IsIconic")
)

// windowVisible reports whether the main window is shown and not minimized.
func windowVisible() bool {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	h, _, _ := procFindWindow.Call(uintptr(unsafePointer(cls)), 0)
	if h == 0 {
		return false
	}
	vis, _, _ := procIsWindowVis.Call(h)
	min, _, _ := procIsIconic.Call(h)
	return vis != 0 && min == 0
}

// allowDarkMenus opts this process into dark popup menus when Windows uses
// the dark app theme (uxtheme ordinals 135 SetPreferredAppMode and 136
// FlushMenuThemes, Windows 10 1903 and later). It does nothing elsewhere.
func allowDarkMenus() {
	if maj, _, build := windows.RtlGetNtVersionNumbers(); maj < 10 || build < 18362 {
		return
	}
	h, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return
	}
	if p, err := windows.GetProcAddressByOrdinal(h, 135); err == nil {
		const allowDark = 1
		syscallN(p, allowDark)
	}
	if p, err := windows.GetProcAddressByOrdinal(h, 136); err == nil {
		syscallN(p)
	}
}
