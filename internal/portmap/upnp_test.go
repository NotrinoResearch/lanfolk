package portmap

import (
	"strings"
	"testing"
	"time"
)

// upnpMapper returns a mapper whose PCP/NAT-PMP attempts fail fast (a silent
// gateway) so that UPnP is reached, with SSDP replaced by the fake's root URL.
func upnpMapper(t *testing.T, igd *fakeIGD, lifetime time.Duration) *Mapper {
	t.Helper()
	gw := newFakePMP(t)
	gw.set(func(f *fakePMP) { f.silent = true })
	m := newTestMapper(t, gw, lifetime, nil)
	m.attemptTimeout = 150 * time.Millisecond
	m.opts.UPnPRootURL = igd.rootURL()
	return m
}

func TestUPnPLifecycle(t *testing.T) {
	for _, st := range []string{
		"urn:schemas-upnp-org:service:WANIPConnection:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
	} {
		t.Run(st[strings.LastIndex(st, ":service:")+9:], func(t *testing.T) {
			igd := newFakeIGD(t, st)
			m := upnpMapper(t, igd, 2*time.Second)
			events := watch(m)
			stop := start(t, m)

			ev := recv(t, events, "mapping")
			if !ev.ok || ev.mp.Protocol != ProtoUPnP || ev.mp.External != ap("203.0.113.9:41641") ||
				ev.mp.Lifetime != 2*time.Second {
				t.Fatalf("unexpected event %+v", ev)
			}

			calls := igd.snapshot()
			if len(calls) < 2 || calls[0].action != "GetExternalIPAddress" || calls[1].action != "AddPortMapping" {
				t.Fatalf("unexpected call sequence %+v", calls)
			}
			want := map[string]string{
				"NewRemoteHost":             "",
				"NewExternalPort":           "41641",
				"NewProtocol":               "UDP",
				"NewInternalPort":           "41641",
				"NewInternalClient":         "127.0.0.1",
				"NewEnabled":                "1",
				"NewPortMappingDescription": "Lanfolk",
				"NewLeaseDuration":          "2",
			}
			got := calls[1].args
			for k, v := range want {
				if g, ok := got[k]; !ok || g != v {
					t.Errorf("AddPortMapping %s = %q (present=%v), want %q", k, g, ok, v)
				}
			}

			// Renewal at half the lease.
			waitFor(t, 3*time.Second, "renewal", func() bool { return igd.count("AddPortMapping") >= 2 })

			stop()
			calls = igd.snapshot()
			last := calls[len(calls)-1]
			if last.action != "DeletePortMapping" {
				t.Fatalf("last call is %s, want DeletePortMapping", last.action)
			}
			if last.args["NewExternalPort"] != "41641" || last.args["NewProtocol"] != "UDP" || last.args["NewRemoteHost"] != "" {
				t.Errorf("DeletePortMapping args %+v", last.args)
			}
			if _, ok := m.Current(); ok {
				t.Error("mapping still reported after shutdown")
			}
		})
	}
}

func TestUPnPRetriesWithPermanentLease(t *testing.T) {
	igd := newFakeIGD(t, "urn:schemas-upnp-org:service:WANIPConnection:1")
	igd.mu.Lock()
	igd.rejectLeases = true
	igd.mu.Unlock()
	m := upnpMapper(t, igd, 2*time.Second)
	events := watch(m)
	start(t, m)

	ev := recv(t, events, "mapping")
	if !ev.ok || ev.mp.Protocol != ProtoUPnP || ev.mp.Lifetime != 0 || ev.mp.External != ap("203.0.113.9:41641") {
		t.Fatalf("unexpected event %+v", ev)
	}
	var leases []string
	for _, c := range igd.snapshot() {
		if c.action == "AddPortMapping" {
			leases = append(leases, c.args["NewLeaseDuration"])
		}
	}
	if strings.Join(leases, ",") != "2,0" {
		t.Fatalf("lease sequence %v, want 2 then 0", leases)
	}

	// A permanent lease is still re-asserted periodically (half the
	// requested lifetime), now without trying a timed lease again.
	waitFor(t, 3*time.Second, "renewal", func() bool { return igd.count("AddPortMapping") >= 3 })
	leases = nil
	for _, c := range igd.snapshot() {
		if c.action == "AddPortMapping" {
			leases = append(leases, c.args["NewLeaseDuration"])
		}
	}
	if strings.Join(leases, ",") != "2,0,0" {
		t.Fatalf("lease sequence %v, want 2,0,0", leases)
	}
}

func TestUPnPUnspecifiedExternalIPIsAFailure(t *testing.T) {
	igd := newFakeIGD(t, "urn:schemas-upnp-org:service:WANIPConnection:1")
	igd.mu.Lock()
	igd.extIP = "0.0.0.0"
	igd.mu.Unlock()
	m := upnpMapper(t, igd, time.Hour)
	events := watch(m)
	start(t, m)

	time.Sleep(600 * time.Millisecond)
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
	if n := igd.count("AddPortMapping"); n != 0 {
		t.Errorf("AddPortMapping called %d times although the router has no external address", n)
	}
}

// Shutdown during a renewal must not leave a mapping on the router: the
// delete has to arrive after the renewal took effect.
func TestShutdownDuringRenewalDeletesLast(t *testing.T) {
	igd := newFakeIGD(t, "urn:schemas-upnp-org:service:WANIPConnection:1")
	m := upnpMapper(t, igd, 2*time.Second)
	events := watch(m)
	stop := start(t, m)
	recv(t, events, "mapping")
	igd.mu.Lock()
	igd.addDelay = 400 * time.Millisecond
	before := igd.addsStarted
	igd.mu.Unlock()
	waitFor(t, 3*time.Second, "renewal request", func() bool {
		igd.mu.Lock()
		defer igd.mu.Unlock()
		return igd.addsStarted > before
	})
	stop() // while the router is still working on the renewal
	time.Sleep(600 * time.Millisecond)
	calls := igd.snapshot()
	if last := calls[len(calls)-1]; last.action != "DeletePortMapping" {
		t.Fatalf("last call is %s, want DeletePortMapping: the mapping stays on the router", last.action)
	}
}
