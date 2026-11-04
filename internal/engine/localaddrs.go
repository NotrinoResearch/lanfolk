package engine

import (
	"context"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"
)

const (
	// localAddrsPoll re-reads the addresses in case a change notification
	// was missed (or the platform has none).
	localAddrsPoll = 30 * time.Second
	// localAddrsSettle lets a burst of notifications (an adapter coming up
	// adds several addresses) end before the addresses are read.
	localAddrsSettle = 300 * time.Millisecond
)

// localAddrs caches the unicast addresses of the interfaces that are up.
// Reading them is slow on Windows (15-27 ms with package net on a machine
// with 10 adapters), so it never runs on the engine loop: it runs at start,
// after an OS change notification and every localAddrsPoll.
type localAddrs struct {
	cur  atomic.Pointer[[]netip.Addr]
	kick chan struct{}
}

func newLocalAddrs() *localAddrs {
	l := &localAddrs{kick: make(chan struct{}, 1)}
	l.refresh()
	return l
}

// get returns the cached addresses. The slice must not be modified.
func (l *localAddrs) get() []netip.Addr {
	if p := l.cur.Load(); p != nil {
		return *p
	}
	return nil
}

// refresh reads the addresses and reports whether they changed.
func (l *localAddrs) refresh() bool {
	addrs, err := listLocalAddrs()
	if err != nil {
		return false
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	addrs = slices.Compact(addrs)
	if old := l.cur.Load(); old != nil && slices.Equal(*old, addrs) {
		return false
	}
	l.cur.Store(&addrs)
	return true
}

func (l *localAddrs) poke() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// run keeps the cache current until ctx ends and calls changed after each
// change.
func (l *localAddrs) run(ctx context.Context, changed func()) {
	stop := watchAddrChanges(l.poke)
	defer stop()
	t := time.NewTicker(localAddrsPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(localAddrsSettle):
			}
			// Drain a notification that arrived while settling.
			select {
			case <-l.kick:
			default:
			}
		case <-t.C:
		}
		if l.refresh() {
			changed()
		}
	}
}
