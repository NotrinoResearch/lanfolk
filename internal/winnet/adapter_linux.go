//go:build linux

package winnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"golang.zx2c4.com/wireguard/tun"
)

// linuxAdapter configures a TUN interface with the ip tool. It exists for
// test clients in network namespaces. Metrics and the network profile have no
// Linux counterpart that matters there, so those calls are no-ops.
type linuxAdapter struct {
	name     string
	warnings []string
}

// NewAdapter wraps the TUN device dev. The interface name comes from the
// device.
func NewAdapter(dev tun.Device) (Adapter, error) {
	if dev == nil {
		return nil, errors.New("winnet: nil tun device")
	}
	name, err := dev.Name()
	if err != nil {
		return nil, fmt.Errorf("winnet: tun device name: %w", err)
	}
	if err := validLinuxIfName(name); err != nil {
		return nil, err
	}
	return &linuxAdapter{name: name}, nil
}

func (a *linuxAdapter) Configure(addr netip.Prefix, mtu int) error {
	if err := validateConfigure(addr, mtu); err != nil {
		return err
	}
	for _, c := range linuxConfigureCommands(a.name, addr, mtu) {
		if err := run(context.Background(), c); err != nil {
			return err
		}
	}
	// Lanfolk is IPv4 only. Failing to disable IPv6 (for example in a
	// namespace with a read-only /proc/sys) is not fatal.
	if err := os.WriteFile(linuxIPv6DisablePath(a.name), []byte("1"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.warnings = append(a.warnings, "disable IPv6: "+err.Error())
	}
	return nil
}

// SetCompatMode is a no-op on Linux: there is no preferred-interface problem
// for broadcast in the test namespaces.
func (a *linuxAdapter) SetCompatMode(bool) error { return nil }

// SetPrivate is a no-op on Linux.
func (a *linuxAdapter) SetPrivate() error { return nil }

func (a *linuxAdapter) Index() int {
	ifc, err := net.InterfaceByName(a.name)
	if err != nil {
		return 0
	}
	return ifc.Index
}

func (a *linuxAdapter) Close() error { return nil }

// Warnings implements Warner.
func (a *linuxAdapter) Warnings() []string { return a.warnings }
