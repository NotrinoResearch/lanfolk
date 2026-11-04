package portmap

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

const upnpDescription = "Lanfolk"

// igdClient is the subset of WANIPConnection1/2 and WANPPPConnection1 used
// here; all three generated clients implement it.
type igdClient interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, protocol string,
		intPort uint16, intClient string, enabled bool, description string, lease uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, protocol string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
}

// igd is a discovered IGD service together with the host serving it.
type igd struct {
	igdClient
	host netip.Addr // address of the root device, if it is an IP literal
	kind string
}

type upnp struct {
	m         *Mapper
	gw, local netip.Addr

	client    *igd   // chosen service once a mapping succeeded
	extPort   uint16 // external port currently mapped
	permanent bool   // router only accepts lease 0
	candidate []*igd
	discovery bool
}

func newUPnP(m *Mapper, gw, local netip.Addr) *upnp {
	return &upnp{m: m, gw: gw, local: local}
}

func (b *upnp) proto() Protocol { return ProtoUPnP }

func (b *upnp) acquire(ctx context.Context) (acquired, error) {
	if b.client != nil {
		// Renewal: reuse the service found earlier.
		res, err := b.addMapping(ctx, b.client)
		if err == nil {
			return res, nil
		}
		b.client = nil // fall through to a fresh discovery
	}
	if !b.discovery {
		dctx, cancel := context.WithTimeout(ctx, b.m.upnpWait)
		b.candidate = b.discover(dctx)
		cancel()
		b.discovery = true
	}
	if len(b.candidate) == 0 {
		return acquired{}, errors.New("upnp: no internet gateway device found")
	}
	var errs []error
	for _, c := range b.candidate {
		res, err := b.addMapping(ctx, c)
		if err == nil {
			b.client = c
			return res, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", c.kind, err))
	}
	b.discovery = false // rediscover next time
	return acquired{}, errors.Join(errs...)
}

// addMapping verifies the external address and then creates the mapping,
// retrying with a permanent lease (0) when the router rejects a timed lease
// and with a random external port when the port is taken.
func (b *upnp) addMapping(ctx context.Context, c *igd) (acquired, error) {
	extStr, err := c.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return acquired{}, fmt.Errorf("GetExternalIPAddress: %w", err)
	}
	extIP, err := netip.ParseAddr(extStr)
	if err != nil || extIP.IsUnspecified() {
		return acquired{}, fmt.Errorf("gateway reports no external address (%q)", extStr)
	}
	extIP = extIP.Unmap()

	want := uint32(lifetimeSeconds(b.m.opts.Lifetime))
	leases := []uint32{want, 0}
	if b.permanent {
		leases = []uint32{0}
	}
	b.m.mu.Lock()
	first := b.m.lastExt
	b.m.mu.Unlock()
	if b.extPort != 0 {
		first = b.extPort
	}
	if first == 0 {
		first = b.m.localPort
	}
	ports := []uint16{first, uint16(49152 + rand.IntN(65535-49152))}

	var lastErr error
	for _, port := range ports {
		for _, lease := range leases {
			err := c.AddPortMappingCtx(ctx, "", port, "UDP", b.m.localPort, b.local.String(), true, upnpDescription, lease)
			if err != nil {
				lastErr = err
				if ctx.Err() != nil {
					return acquired{}, ctx.Err()
				}
				continue
			}
			b.extPort = port
			b.permanent = lease == 0
			return acquired{
				ext:      netip.AddrPortFrom(extIP, port),
				lifetime: time.Duration(lease) * time.Second,
			}, nil
		}
	}
	return acquired{}, fmt.Errorf("AddPortMapping: %w", lastErr)
}

func (b *upnp) release(ctx context.Context) error {
	if b.client == nil || b.extPort == 0 {
		return nil
	}
	return b.client.DeletePortMappingCtx(ctx, "", b.extPort, "UDP")
}

// discover finds IGD services, preferring the ones served by the gateway.
func (b *upnp) discover(ctx context.Context) []*igd {
	var found []*igd
	if root := b.m.opts.UPnPRootURL; root != "" {
		u, err := url.Parse(root)
		if err != nil {
			b.m.log.Info("portmap: bad UPnP root URL", "err", err)
			return nil
		}
		found = igdByURL(ctx, u)
	} else {
		found = igdBySSDP(ctx)
	}
	if b.m.opts.UPnPRootURL != "" || !b.gw.IsValid() {
		return found
	}
	var mine, other []*igd
	for _, c := range found {
		if c.host == b.gw {
			mine = append(mine, c)
		} else {
			other = append(other, c)
		}
	}
	return append(mine, other...)
}

func hostAddr(u *url.URL) netip.Addr {
	if u == nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(u.Hostname())
	return a.Unmap()
}

// igdByURL builds clients for a known root device URL.
func igdByURL(ctx context.Context, u *url.URL) []*igd {
	var out []*igd
	if cs, err := internetgateway2.NewWANIPConnection2ClientsByURLCtx(ctx, u); err == nil {
		for _, c := range cs {
			out = append(out, &igd{c, hostAddr(c.Location), "WANIPConnection2"})
		}
	}
	if cs, err := internetgateway2.NewWANIPConnection1ClientsByURLCtx(ctx, u); err == nil {
		for _, c := range cs {
			out = append(out, &igd{c, hostAddr(c.Location), "WANIPConnection1"})
		}
	}
	if cs, err := internetgateway2.NewWANPPPConnection1ClientsByURLCtx(ctx, u); err == nil {
		for _, c := range cs {
			out = append(out, &igd{c, hostAddr(c.Location), "WANPPPConnection1"})
		}
	}
	return out
}

// igdBySSDP discovers services on the LAN; the three searches run in parallel.
func igdBySSDP(ctx context.Context) []*igd {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		res [3][]*igd
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cs {
			res[0] = append(res[0], &igd{c, hostAddr(c.Location), "WANIPConnection2"})
		}
	}()
	go func() {
		defer wg.Done()
		cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cs {
			res[1] = append(res[1], &igd{c, hostAddr(c.Location), "WANIPConnection1"})
		}
	}()
	go func() {
		defer wg.Done()
		cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cs {
			res[2] = append(res[2], &igd{c, hostAddr(c.Location), "WANPPPConnection1"})
		}
	}()
	wg.Wait()
	return append(append(res[0], res[1]...), res[2]...)
}
