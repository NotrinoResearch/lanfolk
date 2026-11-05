//go:build windows

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/engine"
	"github.com/NotrinoResearch/lanfolk/internal/winnet"
	"golang.org/x/sys/windows/registry"
)

// directPlayCheck is how often the guard compares the active network with
// DirectPlay's setting. A check without a change reads one registry value.
const directPlayCheck = 2 * time.Second

// startDirectPlayGuard keeps DirectPlay's UPnP off for the owner account while
// a network is active (D-038). The returned function waits until the setting
// is restored after ctx ends.
func startDirectPlayGuard(ctx context.Context, e *engine.Engine, dataDir string, log *slog.Logger) (wait func()) {
	g := &winnet.DirectPlayGuard{Root: registry.USERS, SID: e.OwnerSID,
		State: winnet.DirectPlayStateFile(dataDir), Log: log}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(directPlayCheck)
		defer t.Stop()
		for {
			g.Sync(e.NetworkActive())
			select {
			case <-ctx.Done():
				g.Sync(false)
				return
			case <-t.C:
			}
		}
	}()
	return func() { <-done }
}
