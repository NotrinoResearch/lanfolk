//go:build !windows

package engine

import (
	"net"
	"net/netip"
)

// listLocalAddrs reads the unicast addresses of the interfaces that are up.
func listLocalAddrs() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if pfx, err := netip.ParsePrefix(a.String()); err == nil {
				out = append(out, pfx.Addr().Unmap())
			}
		}
	}
	return out, nil
}

// watchAddrChanges has no notification source here; the poll covers it.
func watchAddrChanges(func()) (stop func()) { return func() {} }
