package engine

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

// listLocalAddrs must find the same IPv4 addresses as package net does for
// interfaces that are up (the Windows version uses a single adapter query).
func TestListLocalAddrsMatchesNet(t *testing.T) {
	got, err := listLocalAddrs()
	if err != nil {
		t.Fatal(err)
	}
	have := map[netip.Addr]bool{}
	for _, a := range got {
		if a.IsLoopback() {
			t.Fatalf("loopback address %v listed", a)
		}
		have[a] = true
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			if ip.Is4() && !ip.IsLinkLocalUnicast() && !have[ip] {
				t.Errorf("%v on %s missing", ip, ifc.Name)
			}
		}
	}
}

func TestLocalAddrsReportsChangesOnly(t *testing.T) {
	l := newLocalAddrs()
	if len(l.get()) == 0 {
		t.Skip("no network interfaces")
	}
	if l.refresh() {
		t.Fatal("unchanged addresses reported as a change")
	}
	l.cur.Store(&[]netip.Addr{}) // pretend the machine was offline
	if !l.refresh() {
		t.Fatal("change not reported")
	}
}

func TestLocalAddrsPokeRefreshes(t *testing.T) {
	l := newLocalAddrs()
	if len(l.get()) == 0 {
		t.Skip("no network interfaces")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	go l.run(ctx, func() { changed <- struct{}{} })
	l.cur.Store(&[]netip.Addr{})
	l.poke()
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("poke did not refresh")
	}
}

func BenchmarkListLocalAddrs(b *testing.B) {
	for b.Loop() {
		listLocalAddrs()
	}
}

// The old method, for comparison.
func BenchmarkNetInterfacesAddrs(b *testing.B) {
	for b.Loop() {
		ifaces, _ := net.Interfaces()
		for _, ifc := range ifaces {
			ifc.Addrs()
		}
	}
}
