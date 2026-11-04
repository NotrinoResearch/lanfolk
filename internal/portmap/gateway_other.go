//go:build !linux && !windows

package portmap

import (
	"errors"
	"net/netip"
)

// DefaultGateway is not implemented on this platform; callers can supply
// Options.Gateway instead.
func DefaultGateway() (netip.Addr, error) {
	return netip.Addr{}, errors.New("portmap: default gateway lookup is not supported on this platform")
}
