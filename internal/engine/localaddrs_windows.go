package engine

import (
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// listLocalAddrs reads the unicast addresses of all adapters that are up
// with a single GetAdaptersAddresses call (package net makes one per
// interface).
func listLocalAddrs() ([]netip.Addr, error) {
	ifs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC,
		winipcfg.GAAFlagSkipAnycast|winipcfg.GAAFlagSkipMulticast|winipcfg.GAAFlagSkipDNSServer|
			winipcfg.GAAFlagSkipFriendlyName|winipcfg.GAAFlagSkipDNSInfo)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifs {
		if ifc.OperStatus != winipcfg.IfOperStatusUp || ifc.IfType == winipcfg.IfTypeSoftwareLoopback {
			continue
		}
		for ua := ifc.FirstUnicastAddress; ua != nil; ua = ua.Next {
			if ip, ok := netip.AddrFromSlice(ua.Address.IP()); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out, nil
}

// watchAddrChanges calls poke when an address is added or removed or an
// interface changes state (Wi-Fi switch, cable unplugged, VPN up). poke runs
// on an OS thread and must not block.
func watchAddrChanges(poke func()) (stop func()) {
	ua, err1 := winipcfg.RegisterUnicastAddressChangeCallback(
		func(winipcfg.MibNotificationType, *winipcfg.MibUnicastIPAddressRow) { poke() })
	ic, err2 := winipcfg.RegisterInterfaceChangeCallback(
		func(winipcfg.MibNotificationType, *winipcfg.MibIPInterfaceRow) { poke() })
	return func() {
		if err1 == nil {
			ua.Unregister()
		}
		if err2 == nil {
			ic.Unregister()
		}
	}
}
