// Command ui is the Lanfolk desktop application: a Wails window that is a
// client of the Lanfolk Windows service. It stores no data itself.
//
// Build (no wails CLI or npm needed; the frontend is plain HTML, CSS and JS):
//
//	go build -tags desktop,production -ldflags "-H windowsgui -s -w" -o Lanfolk.exe ./ui
//
// Use -mock to preview the UI against an in-process fake service.
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
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	appTitle       = "Lanfolk"
	instanceID     = "org.notrinoresearch.lanfolk.ui"
	mockInstanceID = instanceID + ".mock"
)

func main() {
	mock := flag.Bool("mock", false, "preview the UI against a built-in fake service")
	pipe := flag.String("pipe", "", "service pipe address (default: the Lanfolk service pipe)")
	flag.Parse()

	addr := *pipe
	if addr == "" {
		addr = ipc.DefaultAddress()
	}
	id := instanceID
	if *mock {
		stop, mockAddr, err := startMock()
		if err != nil {
			fatal(err)
		}
		defer stop()
		addr, id = mockAddr, mockInstanceID
	}

	host := &wailsHost{}
	app := NewApp(addr, host)

	err := wails.Run(&options.App{
		Title:     appTitle,
		Width:     1100,
		Height:    720,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Bind: []any{app},
		OnStartup: func(ctx context.Context) {
			host.setContext(ctx)
			app.start(context.Background())
		},
		OnShutdown: func(context.Context) { app.stop() },
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: id,
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				// A second launch brings the running window to the front.
				if ctx := host.context(); ctx != nil {
					wailsruntime.WindowUnminimise(ctx)
					wailsruntime.Show(ctx)
				}
			},
		},
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
		},
	})
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "lanfolk ui:", err)
	os.Exit(1)
}
