package discovery

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
)

// startLocalDHT builds a private DHT of n serving nodes on 127.0.0.1.
func startLocalDHT(t *testing.T, n int) []*DHT {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var nodes []*DHT
	for i := 0; i < n; i++ {
		opts := DHTOptions{Addr: "127.0.0.1:0", NoBootstrapDefaults: true, Serve: true}
		if i > 0 {
			opts.Bootstrap = []string{nodes[0].Addr().String()}
		}
		d, err := StartDHT(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		nodes = append(nodes, d)
		select {
		case <-d.ready:
		case <-time.After(10 * time.Second):
			t.Fatalf("node %d did not finish bootstrapping", i)
		}
	}
	// A real DHT node only hands out nodes it has verified, which takes
	// minutes. Seed the routing tables directly so the test is fast and
	// deterministic.
	for i, a := range nodes {
		for j, b := range nodes {
			if i == j {
				continue
			}
			ap := b.Addr()
			err := a.srv.AddNode(krpc.NodeInfo{
				ID:   b.srv.ID(),
				Addr: krpc.NodeAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, d := range nodes {
		for d.Nodes() < n-1 {
			if time.Now().After(deadline) {
				t.Fatalf("routing tables did not converge: node has %d of %d", d.Nodes(), n-1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return nodes
}

func TestDHTPublishLookup(t *testing.T) {
	nodes := startLocalDHT(t, 6)
	pa, pb := newPairs(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Nothing published yet.
	if _, err := nodes[4].Lookup(ctx, pb, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound before publish, got %v", err)
	}

	rec := Record{
		Published:  time.Now(),
		Candidates: []netip.AddrPort{v4("203.0.113.7:51820"), v4("[2001:db8::1]:51820")},
	}
	if err := nodes[1].Publish(ctx, pa, rec); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := nodes[4].Lookup(ctx, pb, time.Hour)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Published.Unix() != rec.Published.Unix() || len(got.Candidates) != 2 ||
		got.Candidates[0] != rec.Candidates[0] || got.Candidates[1] != rec.Candidates[1] {
		t.Fatalf("got %+v want %+v", got, rec)
	}

	// Republishing in the same second must still win (sequence number is
	// raised above the one on the network).
	rec2 := Record{Published: rec.Published, Candidates: []netip.AddrPort{v4("198.51.100.9:4000")}}
	if err := nodes[2].Publish(ctx, pa, rec2); err != nil {
		t.Fatalf("republish: %v", err)
	}
	got, err = nodes[5].Lookup(ctx, pb, time.Hour)
	if err != nil {
		t.Fatalf("lookup after republish: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0] != rec2.Candidates[0] {
		t.Fatalf("lookup returned stale record: %+v", got)
	}

	// A different pair does not see the record.
	_, pc := newPairs(t)
	if _, err := nodes[3].Lookup(ctx, pc, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrelated pair: expected ErrNotFound, got %v", err)
	}

	// The publisher cannot read its own slot as a remote record, and the
	// other direction is independent.
	if _, err := nodes[0].Lookup(ctx, pa, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse direction: expected ErrNotFound, got %v", err)
	}
	if err := nodes[4].Publish(ctx, pb, Record{Published: time.Now(), Candidates: []netip.AddrPort{v4("192.0.2.1:1")}}); err != nil {
		t.Fatalf("publish from b: %v", err)
	}
	if got, err = nodes[0].Lookup(ctx, pa, time.Hour); err != nil || got.Candidates[0] != v4("192.0.2.1:1") {
		t.Fatalf("lookup from a: %+v %v", got, err)
	}
}

func TestDHTStaleRecord(t *testing.T) {
	nodes := startLocalDHT(t, 4)
	pa, pb := newPairs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old := Record{Published: time.Now().Add(-3 * time.Hour), Candidates: []netip.AddrPort{v4("203.0.113.7:1")}}
	if err := nodes[1].Publish(ctx, pa, old); err != nil {
		t.Fatal(err)
	}
	if _, err := nodes[2].Lookup(ctx, pb, time.Hour); !errors.Is(err, ErrRecordStale) {
		t.Fatalf("expected ErrRecordStale, got %v", err)
	}
	if _, err := nodes[2].Lookup(ctx, pb, 4*time.Hour); err != nil {
		t.Fatalf("lookup with larger maxAge: %v", err)
	}
}

func TestDHTPublishWithoutNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := StartDHT(ctx, DHTOptions{Addr: "127.0.0.1:0", NoBootstrapDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pa, _ := newPairs(t)
	if err := d.Publish(ctx, pa, Record{Published: time.Now(), Candidates: []netip.AddrPort{v4("1.2.3.4:5")}}); err == nil {
		t.Fatal("publish without any DHT node must fail")
	}
}

func TestStartDHTBadBootstrap(t *testing.T) {
	if _, err := StartDHT(context.Background(), DHTOptions{Addr: "127.0.0.1:0", NoBootstrapDefaults: true, Bootstrap: []string{"nohostport"}}); err == nil {
		t.Fatal("malformed bootstrap address accepted")
	}
}
