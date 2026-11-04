//go:build windows

package portmap

import (
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// DefaultGateway returns the IPv4 default gateway, using GetIpForwardTable2.
// The default route with the lowest combined route and interface metric wins.
func DefaultGateway() (netip.Addr, error) {
	const family = winipcfg.AddressFamily(windows.AF_INET)
	rows, err := winipcfg.GetIPForwardTable2(family)
	if err != nil {
		return netip.Addr{}, err
	}
	var best netip.Addr
	bestMetric := uint64(1<<63 - 1)
	for i := range rows {
		r := &rows[i]
		if r.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		if p := r.DestinationPrefix.Prefix(); !p.Addr().Is4() || !p.Addr().IsUnspecified() {
			continue
		}
		gw := r.NextHop.Addr()
		if !gw.Is4() || gw.IsUnspecified() {
			continue
		}
		metric := uint64(r.Metric)
		if ifc, err := r.InterfaceLUID.IPInterface(family); err == nil {
			metric += uint64(ifc.Metric)
		}
		if metric < bestMetric {
			best, bestMetric = gw, metric
		}
	}
	if !best.IsValid() {
		return netip.Addr{}, errNoGateway
	}
	return best, nil
}
