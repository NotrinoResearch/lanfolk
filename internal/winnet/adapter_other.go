//go:build !windows && !linux

package winnet

import "golang.zx2c4.com/wireguard/tun"

// NewAdapter is not supported on this platform.
func NewAdapter(dev tun.Device) (Adapter, error) {
	return nil, ErrUnsupported
}
