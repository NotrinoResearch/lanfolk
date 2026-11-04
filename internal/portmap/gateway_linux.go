//go:build linux

package portmap

import (
	"net/netip"
	"os"
)

// DefaultGateway returns the IPv4 default gateway, read from /proc/net/route.
func DefaultGateway() (netip.Addr, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	defer f.Close()
	return parseProcNetRoute(f)
}
