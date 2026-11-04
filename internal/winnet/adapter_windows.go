//go:build windows

package winnet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// interfaceWait is how long NewAdapter and Configure wait for the IP stack to
// publish a freshly created Wintun adapter.
const interfaceWait = 5 * time.Second

// windowsAdapter configures the Wintun adapter through the IP Helper API
// (winipcfg). It must run with administrator rights.
type windowsAdapter struct {
	luid  winipcfg.LUID
	index int

	mu       sync.Mutex
	compat   bool
	warnings []string
}

// NewAdapter wraps a Wintun device. dev must be the *tun.NativeTun returned by
// tun.CreateTUN.
func NewAdapter(dev tun.Device) (Adapter, error) {
	nt, ok := dev.(*tun.NativeTun)
	if !ok || nt == nil {
		return nil, fmt.Errorf("winnet: expected *tun.NativeTun, got %T", dev)
	}
	raw := nt.LUID()
	if raw == 0 {
		return nil, errors.New("winnet: tun device is closed")
	}
	a := &windowsAdapter{luid: winipcfg.LUID(raw)}
	row, err := a.waitIPInterface(windows.AF_INET)
	if err != nil {
		return nil, fmt.Errorf("winnet: find adapter: %w", err)
	}
	a.index = int(row.InterfaceIndex)
	return a, nil
}

// waitIPInterface returns the IP interface row of the adapter, retrying while
// the IP stack does not know the adapter yet.
func (a *windowsAdapter) waitIPInterface(family winipcfg.AddressFamily) (*winipcfg.MibIPInterfaceRow, error) {
	deadline := time.Now().Add(interfaceWait)
	for {
		row, err := a.luid.IPInterface(family)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, windows.ERROR_NOT_FOUND) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *windowsAdapter) Configure(addr netip.Prefix, mtu int) error {
	if err := validateConfigure(addr, mtu); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// IPv4 interface settings: MTU, fixed metric, no router discovery and no
	// default route.
	row, err := a.waitIPInterface(windows.AF_INET)
	if err != nil {
		return fmt.Errorf("winnet: get IPv4 interface: %w", err)
	}
	row.NLMTU = uint32(mtu)
	row.UseAutomaticMetric = false
	row.Metric = metricFor(a.compat)
	row.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	row.DisableDefaultRoutes = true
	if err := row.Set(); err != nil {
		return fmt.Errorf("winnet: set IPv4 interface: %w", err)
	}

	// SetIPAddressesForFamily replaces only the IPv4 addresses.
	if err := a.luid.SetIPAddressesForFamily(windows.AF_INET, []netip.Prefix{addr}); err != nil {
		return fmt.Errorf("winnet: set address %s: %w", addr, err)
	}
	// Windows adds the connected route for the new address itself. Make sure
	// it exists; it is the only route the adapter ever gets.
	if err := a.ensureConnectedRoute(addr.Masked()); err != nil {
		return err
	}

	if err := a.disableIPv6(); err != nil {
		a.warnings = append(a.warnings, "disable IPv6: "+err.Error())
	}
	return nil
}

func (a *windowsAdapter) ensureConnectedRoute(prefix netip.Prefix) error {
	nextHop := netip.IPv4Unspecified()
	if _, err := a.luid.Route(prefix, nextHop); err == nil {
		return nil
	}
	if err := a.luid.AddRoute(prefix, nextHop, 0); err != nil &&
		!errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
		return fmt.Errorf("winnet: add route %s: %w", prefix, err)
	}
	return nil
}

// disableIPv6 turns IPv6 off on the adapter as far as the IP Helper API
// allows: no link-local address, no router discovery, no addresses or routes.
// A system without the IPv6 stack has nothing to disable.
func (a *windowsAdapter) disableIPv6() error {
	row, err := a.luid.IPInterface(windows.AF_INET6)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	row.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	row.LinkLocalAddressBehavior = winipcfg.LinkLocalAddressAlwaysOff
	row.DadTransmits = 0
	row.DisableDefaultRoutes = true
	row.UseAutomaticMetric = false
	row.Metric = NormalMetric
	var errs []error
	if err := row.Set(); err != nil {
		errs = append(errs, fmt.Errorf("set interface: %w", err))
	}
	if err := a.luid.FlushIPAddresses(windows.AF_INET6); err != nil {
		errs = append(errs, fmt.Errorf("flush addresses: %w", err))
	}
	if err := a.luid.FlushRoutes(windows.AF_INET6); err != nil {
		errs = append(errs, fmt.Errorf("flush routes: %w", err))
	}
	return errors.Join(errs...)
}

func (a *windowsAdapter) SetCompatMode(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setMetric(on)
}

// setMetric writes the metric for the given mode. The caller holds a.mu.
func (a *windowsAdapter) setMetric(compat bool) error {
	row, err := a.luid.IPInterface(windows.AF_INET)
	if err != nil {
		return fmt.Errorf("winnet: get IPv4 interface: %w", err)
	}
	row.UseAutomaticMetric = false
	row.Metric = metricFor(compat)
	if err := row.Set(); err != nil {
		return fmt.Errorf("winnet: set metric: %w", err)
	}
	a.compat = compat
	return nil
}

func (a *windowsAdapter) SetPrivate() error {
	err := retryUntil(context.Background(), privateRetryTotal, privateRetryInterval,
		func(ctx context.Context) error { return run(ctx, setPrivateCommand(a.index)) })
	if err != nil {
		return fmt.Errorf("winnet: set network profile Private: %w", err)
	}
	return nil
}

func (a *windowsAdapter) Index() int { return a.index }

// Close restores the normal metric if compatibility mode is on. The adapter is
// usually being removed right afterwards, so an adapter that is already gone is
// not an error.
func (a *windowsAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.compat {
		return nil
	}
	err := a.setMetric(false)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	return err
}

// Warnings implements Warner.
func (a *windowsAdapter) Warnings() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.warnings...)
}
