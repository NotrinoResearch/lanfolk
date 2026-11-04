//go:build !windows

package winnet

import "context"

// BroadcastCapture is only functional on Windows.
type BroadcastCapture struct{}

// StartBroadcastCapture is only supported on Windows. Elsewhere there is no
// preferred-interface problem for 255.255.255.255 in the supported test
// setups, and it returns ErrUnsupported.
func StartBroadcastCapture(ctx context.Context, onPacket func(ipv4Packet []byte)) (*BroadcastCapture, error) {
	return nil, ErrUnsupported
}

// Close does nothing.
func (c *BroadcastCapture) Close() error { return nil }

// Err always returns nil.
func (c *BroadcastCapture) Err() error { return nil }

// Done returns a closed channel.
func (c *BroadcastCapture) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
