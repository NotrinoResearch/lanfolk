// Command ui is the Lanfolk desktop application: a compact Wails window plus
// a notification-area (tray) icon. It is a client of the Lanfolk Windows
// service and stores no data itself.
//
// Build (no wails CLI or npm needed; the frontend is plain HTML, CSS and JS):
//
//	go build -tags desktop,production -ldflags "-H windowsgui -s -w" -o Lanfolk.exe ./ui
//
// Flags: -mock previews the UI against an in-process fake service;
// --minimized starts in the tray without showing the window (used at sign-in).
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"os"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	appTitle       = "Lanfolk"
	instanceID     = "org.notrinoresearch.lanfolk.ui"
	mockInstanceID = instanceID + ".mock"
	// windowClass is the Win32 class of the main window; the tray finds the
	// window by it.
	windowClass = "LanfolkWindow"
)

func main() {
	mock := flag.Bool("mock", false, "preview the UI against a built-in fake service")
	pipe := flag.String("pipe", "", "service pipe address (default: the Lanfolk service pipe)")
	minimized := flag.Bool("minimized", false, "start in the notification area without showing the window")
	settings := flag.Bool("settings", false, "open the Settings window (started by the main window)")
	flag.Parse()
	setAppUserModelID()

	addr := *pipe
	if addr == "" {
		addr = ipc.DefaultAddress()
	}
	id := instanceID
	if *mock && !*settings {
		stop, mockAddr, err := startMock()
		if err != nil {
			fatal(err)
		}
		defer stop()
		addr, id = mockAddr, mockInstanceID
	}
	if *pipe != "" && *pipe != ipc.DefaultAddress() {
		id += ".custom" // a preview or test service: do not take the real app's lock
	}
	ws := windowSpec(*settings)
	if *settings {
		processMode = "settings"
	}

	host := &wailsHost{gatePeers: !*settings}
	onWindowShown = host.windowShown
	var tr *tray
	app := NewApp(addr, teeHost{Host: host, also: func(e string, d any) {
		if tr != nil {
			tr.observe(e, d)
		}
	}})
	app.mode = processMode
	app.automation = automationAddress()
	if !*settings {
		tr = newTray(app, host)
	}

	// A running instance that does not respond would swallow this start
	// (see instance_windows.go); end it first so this one takes over.
	endHungInstance(id+ws.InstanceSuffix, windowClassFor(*settings))

	bg := &options.RGBA{R: 243, G: 243, B: 243, A: 255}
	if systemUsesDarkTheme() {
		bg = &options.RGBA{R: 32, G: 32, B: 32, A: 255}
	}
	err := wails.Run(&options.App{
		Title:             ws.Title,
		Width:             ws.Width,
		Height:            ws.Height,
		MinWidth:          ws.MinWidth,
		MinHeight:         ws.MinHeight,
		Frameless:         true,
		StartHidden:       *minimized && !*settings,
		HideWindowOnClose: ws.HideOnClose, // the main window closes to the tray
		BackgroundColour:  bg,
		AssetServer:       &assetserver.Options{Assets: assets},
		Bind:              append([]any{app}, devBindings()...),
		OnStartup: func(ctx context.Context) {
			host.setContext(ctx)
			app.start(context.Background())
			if tr != nil {
				tr.start()
			}
			startAutomation(host, tr)
		},
		OnShutdown: func(context.Context) {
			if tr != nil {
				tr.stop()
			}
			app.stop()
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: id + ws.InstanceSuffix,
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				// Starting Lanfolk again (Start menu, desktop, the gear in the
				// main window) shows the running window.
				if ctx := host.context(); ctx != nil {
					showWindow(ctx)
				}
			},
		},
		Windows: &windows.Options{
			Theme:           windows.SystemDefault,
			WindowClassName: windowClassFor(*settings),
			// Keep the native shadow, rounded corners and snap layouts of a
			// framed window while the frontend draws the title bar.
			DisableFramelessWindowDecorations: false,
		},
	})
	if err != nil {
		fatal(err)
	}
}

// onWindowShown runs after showWindow brings the main window up.
var onWindowShown = func() {}

// processMode is "main" or "settings".
var processMode = "main"

func windowClassFor(settings bool) string {
	if settings {
		return windowClass + "Settings"
	}
	return windowClass
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "lanfolk ui:", err)
	os.Exit(1)
}
