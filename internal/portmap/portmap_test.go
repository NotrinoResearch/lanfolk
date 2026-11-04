package portmap

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestNATPMPLifecycleAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pmpFormat bool
	}{
		{"unsupported version in PCP format", false},
		{"unsupported version in NAT-PMP format", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakePMP(t)
			gw.set(func(f *fakePMP) { f.supportPCP = false; f.unsuppAsPMPFormat = tc.pmpFormat })
			m := newTestMapper(t, gw, 2*time.Second, nil)
			events := watch(m)
			stop := start(t, m)

			ev := recv(t, events, "mapping")
			if !ev.ok {
				t.Fatal("first event is a loss")
			}
			if ev.mp.External != ap("203.0.113.7:50000") || ev.mp.Protocol != ProtoNATPMP ||
				ev.mp.Lifetime != 2*time.Second || ev.mp.Obtained.IsZero() {
				t.Fatalf("unexpected mapping %+v", ev.mp)
			}
			if !ev.mp.IsPublic() {
				t.Error("203.0.113.7 should count as public")
			}
			if cur, ok := m.Current(); !ok || cur.External != ev.mp.External {
				t.Fatalf("Current() = %+v, %v", cur, ok)
			}

			// Fallback order: PCP first (refused), then the NAT-PMP
			// external address request, then the map request.
			reqs := gw.snapshot()
			if len(reqs) < 3 {
				t.Fatalf("got %d requests, want at least 3", len(reqs))
			}
			if reqs[0].ver != 2 || reqs[0].op != 1 {
				t.Errorf("first request should be a PCP MAP, got ver=%d op=%d", reqs[0].ver, reqs[0].op)
			}
			if reqs[1].ver != 0 || reqs[1].op != 0 || len(reqs[1].raw) != 2 {
				t.Errorf("second request should be a NAT-PMP external address request: %x", reqs[1].raw)
			}
			mp := reqs[2]
			if mp.ver != 0 || mp.op != 1 || len(mp.raw) != 12 || mp.intPort != testPort ||
				mp.extPort != testPort || mp.lifetime != 2 {
				t.Errorf("bad NAT-PMP map request: %x (%+v)", mp.raw, mp)
			}

			// Renewal at half the granted lifetime, suggesting the port
			// the gateway assigned.
			waitFor(t, 3*time.Second, "renewal", func() bool { return len(gw.maps(0)) >= 2 })
			maps := gw.maps(0)
			gap := maps[1].at.Sub(maps[0].at)
			if gap < 800*time.Millisecond || gap > 1700*time.Millisecond {
				t.Errorf("renewal after %v, want about 1s", gap)
			}
			if maps[1].extPort != 50000 || maps[1].lifetime != 2 {
				t.Errorf("renewal request %+v", maps[1])
			}
			select {
			case ev := <-events:
				t.Fatalf("unchanged mapping must not notify again: %+v", ev)
			default:
			}

			// Shutdown deletes the mapping: lifetime 0, external port 0.
			stop()
			all := gw.maps(0)
			last := all[len(all)-1]
			if last.lifetime != 0 || last.extPort != 0 || last.intPort != testPort || len(last.raw) != 12 {
				t.Errorf("delete request %x (%+v)", last.raw, last)
			}
			if _, ok := m.Current(); ok {
				t.Error("Current() still reports a mapping after shutdown")
			}
			if ev := recv(t, events, "loss"); ev.ok {
				t.Errorf("last event should be a loss, got %+v", ev)
			}
		})
	}
}

func TestPCPLifecycle(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.ext = ap("203.0.113.8:40000"); f.maxLifetime = 2 })
	// Ask for 4 s; the gateway grants 2 s, so renewal must follow 1 s.
	m := newTestMapper(t, gw, 4*time.Second, nil)
	events := watch(m)
	stop := start(t, m)

	ev := recv(t, events, "mapping")
	if !ev.ok || ev.mp.Protocol != ProtoPCP || ev.mp.External != ap("203.0.113.8:40000") ||
		ev.mp.Lifetime != 2*time.Second {
		t.Fatalf("unexpected event %+v", ev)
	}

	first := gw.snapshot()[0]
	if first.ver != 2 || first.op != 1 || len(first.raw) != 60 {
		t.Fatalf("bad PCP request %x", first.raw)
	}
	if first.lifetime != 4 || first.proto != 17 || first.intPort != testPort || first.extPort != testPort {
		t.Errorf("bad PCP MAP fields %+v", first)
	}
	if first.client != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("client address %v", first.client)
	}
	if first.nonce == ([12]byte{}) {
		t.Error("nonce is all zero")
	}
	if ext := first.raw[44:60]; strings.Trim(string(ext), "\x00") != "" {
		t.Errorf("suggested external IP should be zero: %x", ext)
	}

	waitFor(t, 3*time.Second, "renewal", func() bool { return len(gw.maps(2)) >= 2 })
	maps := gw.maps(2)
	if gap := maps[1].at.Sub(maps[0].at); gap < 800*time.Millisecond || gap > 1700*time.Millisecond {
		t.Errorf("renewal after %v, want about 1s", gap)
	}
	if maps[1].nonce != first.nonce || maps[1].extPort != 40000 {
		t.Errorf("renewal must reuse the nonce and suggest the assigned port: %+v", maps[1])
	}

	stop()
	all := gw.maps(2)
	del := all[len(all)-1]
	if del.lifetime != 0 || del.nonce != first.nonce || del.intPort != testPort || del.proto != 17 {
		t.Errorf("delete request %+v", del)
	}
	for _, r := range gw.snapshot() {
		if r.ver == 0 {
			t.Fatalf("NAT-PMP must not be used while PCP works: %x", r.raw)
		}
	}
}

func TestRetransmitsLostRequests(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.drop = 2 })
	m := newTestMapper(t, gw, time.Hour, nil)
	events := watch(m)
	start(t, m)

	ev := recv(t, events, "mapping")
	if !ev.ok || ev.mp.Protocol != ProtoPCP {
		t.Fatalf("unexpected event %+v", ev)
	}
	maps := gw.maps(2)
	if len(maps) != 3 {
		t.Fatalf("got %d PCP requests, want 3 (two lost, one answered)", len(maps))
	}
	if string(maps[0].raw) != string(maps[2].raw) {
		t.Error("retransmissions must be identical")
	}
	if d := maps[2].at.Sub(maps[0].at); d < 250*time.Millisecond {
		t.Errorf("retransmissions came too fast (%v), backoff expected", d)
	}
}

func TestBackoffRetryAfterFailure(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.silent = true })
	m := newTestMapper(t, gw, time.Hour, nil)
	m.attemptTimeout = 150 * time.Millisecond
	events := watch(m)
	start(t, m)

	time.Sleep(700 * time.Millisecond)
	if _, ok := m.Current(); ok {
		t.Fatal("mapping reported while the gateway is silent")
	}
	select {
	case ev := <-events:
		if ev.ok {
			t.Fatalf("unexpected mapping %+v", ev)
		}
	default:
	}
	n := len(gw.snapshot())
	if n < 4 {
		t.Fatalf("only %d requests; the mapper should keep retrying", n)
	}
	gw.set(func(f *fakePMP) { f.silent = false })
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.ok {
				if ev.mp.Protocol != ProtoPCP {
					t.Fatalf("unexpected mapping %+v", ev)
				}
				return
			}
		case <-deadline:
			t.Fatal("mapping not obtained after the gateway recovered")
		}
	}
}

func TestExternalAddressChangeNotifies(t *testing.T) {
	gw := newFakePMP(t)
	m := newTestMapper(t, gw, 2*time.Second, nil)
	events := watch(m)
	start(t, m)

	if ev := recv(t, events, "first mapping"); ev.mp.External != ap("203.0.113.7:50000") {
		t.Fatalf("unexpected %+v", ev)
	}
	gw.set(func(f *fakePMP) { f.ext = ap("198.51.100.4:51000") })
	ev := recv(t, events, "changed mapping")
	if !ev.ok || ev.mp.External != ap("198.51.100.4:51000") {
		t.Fatalf("unexpected %+v", ev)
	}
	if cur, _ := m.Current(); cur.External != ev.mp.External {
		t.Errorf("Current() = %+v", cur)
	}
}

func TestNonPublicExternalIsKeptAndLogged(t *testing.T) {
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.ext = ap("100.64.3.2:50000") })
	var logs syncBuf
	m := newTestMapper(t, gw, time.Hour, &logs)
	events := watch(m)
	start(t, m)

	ev := recv(t, events, "mapping")
	if !ev.ok || ev.mp.IsPublic() {
		t.Fatalf("CGNAT mapping must be kept and flagged non-public: %+v", ev)
	}
	if !strings.Contains(logs.String(), "not public") {
		t.Errorf("expected a log line about the non-public address, got:\n%s", logs.String())
	}
}

func TestIsPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"203.0.113.7":     true,
		"8.8.8.8":         true,
		"2001:db8::1":     true,
		"10.1.2.3":        false,
		"172.16.0.1":      false,
		"192.168.1.1":     false,
		"100.64.0.1":      false,
		"100.127.255.1":   false,
		"100.128.0.1":     true,
		"169.254.1.1":     false,
		"127.0.0.1":       false,
		"0.0.0.0":         false,
		"fd00::1":         false,
		"::ffff:10.0.0.1": false,
	} {
		if got := IsPublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsPublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
	if IsPublicAddr(netip.Addr{}) {
		t.Error("invalid address is not public")
	}
}

func TestNothingBlocksWhenNoGateway(t *testing.T) {
	m := New(testPort, Options{
		Gateway:     func() (netip.Addr, error) { return netip.Addr{}, errNoGateway },
		UPnPRootURL: "http://127.0.0.1:1/x.xml",
	})
	m.backoffMin, m.backoffMax = 50*time.Millisecond, 100*time.Millisecond
	stop := start(t, m)
	time.Sleep(200 * time.Millisecond)
	if _, ok := m.Current(); ok {
		t.Fatal("unexpected mapping")
	}
	stop()
}

func TestEncodingGolden(t *testing.T) {
	nonce := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	got := encodePCPMapRequest(netip.MustParseAddr("192.168.1.20"), nonce, 41641, 41641, 7200)
	want := []byte{
		2, 1, 0, 0, // version, MAP, reserved
		0, 0, 0x1c, 0x20, // lifetime 7200
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 168, 1, 20, // client IP
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, // nonce
		17, 0, 0, 0, // UDP, reserved
		0xa2, 0xa9, 0xa2, 0xa9, // internal and suggested external port
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // suggested external IP
	}
	if string(got) != string(want) {
		t.Errorf("PCP request\n got %x\nwant %x", got, want)
	}
	gotP := encodeNATPMPMapRequest(41641, 41642, 7200)
	wantP := []byte{0, 1, 0, 0, 0xa2, 0xa9, 0xa2, 0xaa, 0, 0, 0x1c, 0x20}
	if string(gotP) != string(wantP) {
		t.Errorf("NAT-PMP request\n got %x\nwant %x", gotP, wantP)
	}
}

func TestDecodeRejectsUnrelatedPackets(t *testing.T) {
	nonce := [12]byte{9}
	resp := make([]byte, 60)
	resp[0], resp[1] = 2, 0x81
	copy(resp[24:36], nonce[:])
	resp[36] = 17
	resp[40], resp[41] = 0xa2, 0xa9
	if _, err := decodePCPMap(resp, nonce, 41641); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	if _, err := decodePCPMap(resp, [12]byte{1}, 41641); err != errIgnore {
		t.Errorf("wrong nonce: %v", err)
	}
	if _, err := decodePCPMap(resp, nonce, 1234); err != errIgnore {
		t.Errorf("wrong port: %v", err)
	}
	if _, err := decodePCPMap(resp[:30], nonce, 41641); err != errIgnore {
		t.Errorf("truncated: %v", err)
	}
	if _, err := decodeNATPMPMap([]byte{0, 129, 0, 0}, 41641); err != errIgnore {
		t.Errorf("truncated NAT-PMP: %v", err)
	}
	if _, err := decodeNATPMPMap([]byte{0, 129, 0, 2, 0, 0, 0, 0}, 41641); err == nil || err == errIgnore {
		t.Errorf("NOT_AUTHORIZED must be reported, got %v", err)
	}
}
