// Package portmap asks the home gateway to forward the local UDP port of
// Lanfolk's single socket, using PCP (RFC 6887), NAT-PMP (RFC 6886) and UPnP
// IGD in that order. A successful mapping yields an external address that is
// advertised as an extra direct candidate ("Automatic port").
//
// Everything here is best effort. Lanfolk works without a mapping, so no
// failure is fatal and nothing blocks the caller: Run is meant to be started
// in its own goroutine and all other methods return immediately.
package portmap

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Protocol names the mechanism that produced a mapping.
type Protocol string

// Supported protocols.
const (
	ProtoPCP    Protocol = "pcp"
	ProtoNATPMP Protocol = "nat-pmp"
	ProtoUPnP   Protocol = "upnp"
)

// DefaultLifetime is the mapping lifetime requested when Options.Lifetime is
// zero.
const DefaultLifetime = 2 * time.Hour

const (
	defaultPMPPort    = 5351
	defaultBackoffMin = time.Minute
	defaultBackoffMax = 30 * time.Minute
	defaultAttempt    = 3 * time.Second // total wait per PCP / NAT-PMP attempt
	defaultUPnPWait   = 4 * time.Second // SSDP discovery budget
	defaultInitialRTO = 250 * time.Millisecond
	minRenewIn        = 500 * time.Millisecond
	releaseTimeout    = 2 * time.Second
)

// Mapping is an active port mapping on the gateway.
type Mapping struct {
	// External is the gateway's external address and port for the local
	// UDP port.
	External netip.AddrPort
	Protocol Protocol
	// Lifetime is the lease granted by the gateway. Zero means the
	// gateway granted a permanent UPnP lease.
	Lifetime time.Duration
	// Obtained is when the gateway last confirmed the mapping.
	Obtained time.Time
}

// IsPublic reports whether the external address is a public Internet
// address. A mapping on a private (RFC 1918) or carrier-grade NAT
// (100.64.0.0/10) address is only reachable from peers behind the same
// carrier network, so it is a weak candidate.
func (m Mapping) IsPublic() bool { return IsPublicAddr(m.External.Addr()) }

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// IsPublicAddr reports whether a is a globally routable unicast address that
// is not RFC 1918 / ULA private and not in the CGNAT range 100.64.0.0/10.
func IsPublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !cgnatPrefix.Contains(a)
}

// Options configures a Mapper. The zero value is usable.
type Options struct {
	// Logger receives progress messages. nil discards them.
	Logger *slog.Logger
	// Gateway returns the IPv4 gateway to talk to. nil means DefaultGateway.
	Gateway func() (netip.Addr, error)
	// LocalIP returns the local address used to reach gw. nil means the
	// address the OS picks for a route to gw.
	LocalIP func(gw netip.Addr) (netip.Addr, error)
	// Lifetime is the requested mapping lifetime. Default 2h. With UPnP
	// the lease is Lifetime, and 0 (permanent) if the router rejects it.
	Lifetime time.Duration

	// Test hooks.

	// PMPPort overrides UDP port 5351 for PCP and NAT-PMP on the gateway.
	PMPPort int
	// UPnPRootURL skips SSDP and fetches the IGD root description from
	// this URL.
	UPnPRootURL string
}

// Mapper keeps a UDP port mapping alive on the home gateway.
type Mapper struct {
	localPort uint16
	opts      Options
	log       *slog.Logger

	// Tunables, overridden by tests.
	attemptTimeout time.Duration
	upnpWait       time.Duration
	initialRTO     time.Duration
	backoffMin     time.Duration
	backoffMax     time.Duration

	nonce [12]byte // PCP mapping nonce, stable for the life of the Mapper

	mu      sync.Mutex
	cur     Mapping
	have    bool
	cbs     []func(Mapping, bool)
	lastExt uint16 // last external port assigned, suggested again on renewal
}

// New returns a Mapper for the given local UDP port. It does nothing until
// Run is called.
func New(localPort uint16, opts Options) *Mapper {
	m := &Mapper{
		localPort:      localPort,
		opts:           opts,
		log:            opts.Logger,
		attemptTimeout: defaultAttempt,
		upnpWait:       defaultUPnPWait,
		initialRTO:     defaultInitialRTO,
		backoffMin:     defaultBackoffMin,
		backoffMax:     defaultBackoffMax,
	}
	if m.log == nil {
		m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if m.opts.Gateway == nil {
		m.opts.Gateway = DefaultGateway
	}
	if m.opts.LocalIP == nil {
		m.opts.LocalIP = localAddrTo
	}
	if m.opts.Lifetime <= 0 {
		m.opts.Lifetime = DefaultLifetime
	}
	if m.opts.PMPPort == 0 {
		m.opts.PMPPort = defaultPMPPort
	}
	rand.Read(m.nonce[:])
	return m
}

// Current returns the active mapping, if any.
func (m *Mapper) Current() (Mapping, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, m.have
}

// OnChange registers fn, called with (mapping, true) when a mapping is
// obtained or its external address changes, and with (Mapping{}, false) when
// it is lost. Callbacks run on the goroutine of Run, so they should return
// quickly. A callback registered while a mapping exists is not called for
// that mapping; use Current.
func (m *Mapper) OnChange(fn func(Mapping, bool)) {
	m.mu.Lock()
	m.cbs = append(m.cbs, fn)
	m.mu.Unlock()
}

// set stores the mapping and notifies listeners when the external address,
// protocol or presence changed.
func (m *Mapper) set(mp Mapping, ok bool) {
	m.mu.Lock()
	changed := m.have != ok || (ok && (m.cur.External != mp.External || m.cur.Protocol != mp.Protocol))
	m.cur, m.have = mp, ok
	cbs := append([]func(Mapping, bool){}, m.cbs...)
	m.mu.Unlock()
	if !changed {
		return
	}
	for _, fn := range cbs {
		fn(mp, ok)
	}
}

// acquired is the outcome of creating or renewing a mapping.
type acquired struct {
	ext      netip.AddrPort
	lifetime time.Duration // granted; 0 = permanent (UPnP)
}

// backend is one port mapping protocol bound to a gateway.
type backend interface {
	proto() Protocol
	// acquire creates or refreshes the mapping.
	acquire(ctx context.Context) (acquired, error)
	// release removes the mapping, best effort.
	release(ctx context.Context) error
}

// Run keeps a mapping alive until ctx is done. It tries PCP, NAT-PMP and UPnP
// in that order, renews at half the granted lifetime, and when every protocol
// fails it retries with exponential backoff from one to thirty minutes.
// On return the mapping has been deleted on the gateway (best effort).
func (m *Mapper) Run(ctx context.Context) {
	backoff := m.backoffMin
	var be backend
	for ctx.Err() == nil {
		var res acquired
		var err error
		if be != nil {
			res, err = m.attempt(ctx, be)
			if err != nil {
				if ctx.Err() != nil {
					break // keep be so that it is released below
				}
				m.log.Warn("portmap: renewal failed", "protocol", be.proto(), "err", err)
				be = nil
			}
		}
		if be == nil {
			be, res = m.discover(ctx)
		}
		if be == nil {
			if ctx.Err() != nil {
				break
			}
			m.set(Mapping{}, false)
			m.log.Info("portmap: no port mapping protocol available", "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, m.backoffMax)
			continue
		}
		backoff = m.backoffMin
		mp := Mapping{External: res.ext, Protocol: be.proto(), Lifetime: res.lifetime, Obtained: time.Now()}
		m.set(mp, true)
		sleep(ctx, m.renewIn(res.lifetime))
	}
	if be != nil {
		rctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		if err := be.release(rctx); err != nil {
			m.log.Debug("portmap: delete mapping failed", "protocol", be.proto(), "err", err)
		}
		cancel()
	}
	m.set(Mapping{}, false)
}

// renewIn returns how long to wait before renewing a lease of the given
// length. A permanent lease is re-checked at half the requested lifetime.
func (m *Mapper) renewIn(lifetime time.Duration) time.Duration {
	if lifetime <= 0 {
		lifetime = m.opts.Lifetime
	}
	return max(lifetime/2, minRenewIn)
}

// attempt runs one acquire with the per-protocol time cap and records the
// result.
func (m *Mapper) attempt(ctx context.Context, be backend) (acquired, error) {
	limit := m.attemptTimeout
	if be.proto() == ProtoUPnP {
		limit += m.upnpWait
	}
	// A request already on its way could reach the gateway after the delete
	// sent at shutdown and leave the mapping behind (for good, with a
	// permanent UPnP lease). So shutdown does not abandon an attempt: it
	// lets it finish for up to releaseTimeout, and Run releases afterwards.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		t := time.NewTimer(releaseTimeout)
		defer t.Stop()
		select {
		case <-actx.Done():
		case <-t.C:
			cancel()
		}
	})
	defer stop()
	res, err := be.acquire(actx)
	if err != nil {
		return acquired{}, err
	}
	if !res.ext.IsValid() || res.ext.Port() == 0 || res.ext.Addr().IsUnspecified() {
		return acquired{}, fmt.Errorf("gateway returned unusable external address %v", res.ext)
	}
	m.mu.Lock()
	m.lastExt = res.ext.Port()
	m.mu.Unlock()
	if !IsPublicAddr(res.ext.Addr()) {
		m.log.Warn("portmap: external address is not public (CGNAT or double NAT); only peers in the same carrier network can use it",
			"protocol", be.proto(), "external", res.ext)
	}
	return res, nil
}

// discover tries each protocol in turn and returns the first that works.
func (m *Mapper) discover(ctx context.Context) (backend, acquired) {
	gw, err := m.opts.Gateway()
	if err != nil || !gw.IsValid() {
		m.log.Info("portmap: no gateway", "err", err)
		return nil, acquired{}
	}
	gw = gw.Unmap()
	local, err := m.opts.LocalIP(gw)
	if err != nil || !local.IsValid() {
		m.log.Info("portmap: no local address for gateway", "gateway", gw, "err", err)
		return nil, acquired{}
	}
	local = local.Unmap()
	m.mu.Lock()
	suggested := m.lastExt
	m.mu.Unlock()
	if suggested == 0 {
		suggested = m.localPort
	}
	cands := []backend{
		newPCP(m, gw, local, suggested),
		newNATPMP(m, gw, local, suggested),
		newUPnP(m, gw, local),
	}
	for _, be := range cands {
		if ctx.Err() != nil {
			return nil, acquired{}
		}
		res, err := m.attempt(ctx, be)
		if err != nil {
			m.log.Info("portmap: protocol unavailable", "protocol", be.proto(), "gateway", gw, "err", err)
			continue
		}
		m.log.Info("portmap: mapping obtained", "protocol", be.proto(), "external", res.ext, "lifetime", res.lifetime)
		return be, res
	}
	return nil, acquired{}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// localAddrTo returns the local IPv4 address the OS uses to reach gw. No
// packet is sent.
func localAddrTo(gw netip.Addr) (netip.Addr, error) {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: gw.AsSlice(), Port: defaultPMPPort})
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, errors.New("portmap: unexpected local address type")
	}
	a, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return netip.Addr{}, errors.New("portmap: invalid local address")
	}
	return a.Unmap(), nil
}
