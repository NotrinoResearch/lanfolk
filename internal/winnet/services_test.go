package winnet

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceOnly(t *testing.T) {
	m := serviceOnly([]portOwner{
		{ProtoTCP, 22, true},                            // sshd service
		{ProtoUDP, 5353, true}, {ProtoUDP, 5353, false}, // shared with a desktop program
		{ProtoUDP, 27015, false},                       // a game
		{ProtoTCP, 5432, true}, {ProtoTCP, 5432, true}, // two service sockets
		{ProtoUDP, 22, false},
	})
	want := map[uint32]bool{serviceKey(ProtoTCP, 22): true, serviceKey(ProtoTCP, 5432): true}
	if len(m) != len(want) {
		t.Fatalf("got %v, want %v", m, want)
	}
	for k := range want {
		if !m[k] {
			t.Errorf("missing %x", k)
		}
	}
}

func TestServiceFilterRefreshesInBackground(t *testing.T) {
	var loads atomic.Int32
	var fail atomic.Bool
	gate := make(chan struct{}, 8)
	f := newServiceFilter(func() (map[uint32]bool, error) {
		n := loads.Add(1)
		if n > 1 {
			<-gate
		}
		if fail.Load() {
			return nil, errors.New("table unavailable")
		}
		if n == 1 {
			return map[uint32]bool{serviceKey(ProtoTCP, 22): true}, nil
		}
		return map[uint32]bool{serviceKey(ProtoTCP, 3306): true}, nil
	}, 20*time.Millisecond)

	if !f.ServicePort(ProtoTCP, 22) || f.ServicePort(ProtoTCP, 3306) || f.ServicePort(ProtoUDP, 22) {
		t.Fatal("first table not used")
	}
	time.Sleep(30 * time.Millisecond)
	// Stale: the caller still gets the old answer at once and one refresh
	// starts, however many packets ask.
	for range 100 {
		if !f.ServicePort(ProtoTCP, 22) {
			t.Fatal("stale table not used while refreshing")
		}
	}
	gate <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for !f.ServicePort(ProtoTCP, 3306) {
		if time.Now().After(deadline) {
			t.Fatal("refreshed table never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	if n := loads.Load(); n != 2 {
		t.Fatalf("%d loads, want 2 (one background refresh)", n)
	}
	// A failing load keeps the last good table.
	fail.Store(true)
	time.Sleep(30 * time.Millisecond)
	f.ServicePort(ProtoTCP, 3306)
	gate <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	if !f.ServicePort(ProtoTCP, 3306) {
		t.Fatal("a failed refresh dropped the table")
	}
}

func TestServiceFilterWithoutTableAllows(t *testing.T) {
	f := newServiceFilter(func() (map[uint32]bool, error) { return nil, errors.New("no table") }, time.Hour)
	if f.ServicePort(ProtoTCP, 22) {
		t.Fatal("no table must allow")
	}
}

func BenchmarkServicePort(b *testing.B) {
	f := newServiceFilter(func() (map[uint32]bool, error) {
		return map[uint32]bool{serviceKey(ProtoTCP, 22): true}, nil
	}, time.Hour)
	b.ReportAllocs()
	for b.Loop() {
		f.ServicePort(ProtoUDP, 27015)
	}
}
