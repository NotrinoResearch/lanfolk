// Package winnet integrates the Lanfolk virtual adapter with the operating
// system: address, MTU and metric configuration, network profile, firewall
// rules, the shared port denylist and global broadcast capture.
//
// Windows is the production target. A Linux implementation exists so that test
// clients can run in network namespaces; it configures the interface with the
// ip tool and treats metrics and firewall rules as no-ops.
package winnet

import (
	"errors"
	"net/netip"
)

// AdapterName is the name of the Wintun adapter and of the Linux test
// interface.
const AdapterName = "Lanfolk"

// FirewallGroup is the Windows Defender Firewall rule group of all Lanfolk
// rules.
const FirewallGroup = "Lanfolk"

// Interface metrics. Wintun reports a very high link speed, so an automatic
// metric would make Windows prefer the virtual adapter over the physical one.
// NormalMetric keeps the physical adapter preferred. CompatMetric makes the
// virtual adapter preferred, which is what Windows needs to send
// 255.255.255.255 and multicast through it.
const (
	NormalMetric uint32 = 9000
	CompatMetric uint32 = 1
)

// MinMTU is the smallest MTU Configure accepts.
const MinMTU = 576

// MaxMTU is the largest MTU Configure accepts.
const MaxMTU = 65535

// ErrUnsupported is returned by features that do not exist on this platform.
var ErrUnsupported = errors.ErrUnsupported

// Adapter wraps OS configuration of the virtual adapter.
type Adapter interface {
	// Configure assigns addr (e.g. 10.77.0.3/24), sets the MTU, adds no
	// default route, disables IPv6 on the adapter if possible (Lanfolk is IPv4
	// only) and applies the normal metric.
	Configure(addr netip.Prefix, mtu int) error
	// SetCompatMode lowers the adapter metric (compatibility mode, so Windows
	// prefers the adapter for 255.255.255.255 and multicast) or restores the
	// normal high metric.
	SetCompatMode(on bool) error
	// SetPrivate marks the adapter's network as Private. On Windows it waits
	// for the Network Location Awareness service to identify the network and
	// retries for about 15 seconds.
	SetPrivate() error
	// Index returns the interface index for multicast and IP_UNICAST_IF use.
	Index() int
	// Close restores anything changed (the metric) before the adapter is
	// removed.
	Close() error
}

// Warner is implemented by adapters that complete Configure although an
// optional step failed (for example disabling IPv6). The messages are meant
// for the log.
type Warner interface {
	Warnings() []string
}

func metricFor(compat bool) uint32 {
	if compat {
		return CompatMetric
	}
	return NormalMetric
}

func validateConfigure(addr netip.Prefix, mtu int) error {
	if !addr.IsValid() || !addr.Addr().Is4() {
		return errors.New("winnet: address must be an IPv4 prefix such as 10.77.0.3/24")
	}
	if mtu < MinMTU || mtu > MaxMTU {
		return errors.New("winnet: MTU out of range")
	}
	return nil
}
