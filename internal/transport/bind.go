package transport

import (
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
	"golang.zx2c4.com/wireguard/conn"
)

// PeerEndpoint is the WireGuard endpoint of a Lanfolk peer. It names the
// peer, not an address: the path manager picks the address at send time,
// so path switches never wait for WireGuard roaming.
type PeerEndpoint struct {
	Key    identity.PublicKey
	synth  netip.Addr
	dst    string
	keyRaw []byte
}

const peerEndpointPrefix = "lanfolk-peer:"

// NewPeerEndpoint returns the endpoint for key.
func NewPeerEndpoint(key identity.PublicKey) *PeerEndpoint {
	id := key.KeyID()
	var a [16]byte
	a[0], a[1] = 0xfd, 0x4c // unique-local prefix; used only by WireGuard's rate limiter
	copy(a[2:], id[:])
	copy(a[10:], key[:6])
	return &PeerEndpoint{Key: key, synth: netip.AddrFrom16(a), dst: peerEndpointPrefix + key.Hex(), keyRaw: key[:]}
}

func (e *PeerEndpoint) ClearSrc()           {}
func (e *PeerEndpoint) SrcToString() string { return "" }
func (e *PeerEndpoint) DstToString() string { return e.dst }
func (e *PeerEndpoint) DstToBytes() []byte  { return e.keyRaw }
func (e *PeerEndpoint) DstIP() netip.Addr   { return e.synth }
func (e *PeerEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// RawEndpoint is a plain UDP address, used for WireGuard packets that arrive
// from an address not yet tied to a peer.
type RawEndpoint struct{ Addr netip.AddrPort }

func (e *RawEndpoint) ClearSrc()           {}
func (e *RawEndpoint) SrcToString() string { return "" }
func (e *RawEndpoint) DstToString() string { return e.Addr.String() }
func (e *RawEndpoint) DstToBytes() []byte {
	b, _ := e.Addr.MarshalBinary()
	return b
}
func (e *RawEndpoint) DstIP() netip.Addr { return e.Addr.Addr() }
func (e *RawEndpoint) SrcIP() netip.Addr { return netip.Addr{} }

// Sender transmits WireGuard packets on behalf of the Bind.
type Sender interface {
	SendWireGuard(pkt []byte, ep conn.Endpoint) error
}

type inbound struct {
	buf [wire.MaxPacket]byte
	n   int
	ep  conn.Endpoint
}

// Bind is a conn.Bind backed by the shared socket. The engine's read loop
// delivers WireGuard packets with Deliver; WireGuard's sends go to Sender.
type Bind struct {
	sender Sender
	port   uint16
	queue  chan *inbound
	pool   sync.Pool
	mu     sync.Mutex
	open   bool
	done   chan struct{}
}

// bindQueueLen bounds the receive queue. Excess packets are dropped instead
// of building latency.
const bindQueueLen = 256

// NewBind creates a Bind reporting port as its listening port.
func NewBind(sender Sender, port uint16) *Bind {
	b := &Bind{sender: sender, port: port, queue: make(chan *inbound, bindQueueLen)}
	b.pool.New = func() any { return new(inbound) }
	return b
}

// Deliver queues one received WireGuard packet. It never blocks.
func (b *Bind) Deliver(pkt []byte, ep conn.Endpoint) {
	b.mu.Lock()
	open := b.open
	b.mu.Unlock()
	if !open || len(pkt) > wire.MaxPacket {
		return
	}
	in := b.pool.Get().(*inbound)
	in.n = copy(in.buf[:], pkt)
	in.ep = ep
	select {
	case b.queue <- in:
	default:
		b.pool.Put(in)
	}
}

// Open implements conn.Bind.
func (b *Bind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	// Drop anything that was queued while closed.
	for len(b.queue) > 0 {
		b.pool.Put(<-b.queue)
	}
	b.open = true
	b.done = make(chan struct{})
	done := b.done
	fn := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		return b.receive(done, packets, sizes, eps)
	}
	return []conn.ReceiveFunc{fn}, b.port, nil
}

func (b *Bind) fill(in *inbound, packets [][]byte, sizes []int, eps []conn.Endpoint, i int) {
	sizes[i] = copy(packets[i], in.buf[:in.n])
	eps[i] = in.ep
	in.ep = nil
	b.pool.Put(in)
}

func (b *Bind) receive(done chan struct{}, packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
	select {
	case in := <-b.queue:
		b.fill(in, packets, sizes, eps, 0)
	case <-done:
		return 0, net.ErrClosed
	}
	n := 1
	for n < len(packets) {
		select {
		case in := <-b.queue:
			b.fill(in, packets, sizes, eps, n)
			n++
		default:
			return n, nil
		}
	}
	return n, nil
}

// Close implements conn.Bind.
func (b *Bind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		close(b.done)
		b.open = false
	}
	return nil
}

// SetMark implements conn.Bind.
func (b *Bind) SetMark(uint32) error { return nil }

// Send implements conn.Bind.
func (b *Bind) Send(bufs [][]byte, ep conn.Endpoint) error {
	var first error
	for _, buf := range bufs {
		if err := b.sender.SendWireGuard(buf, ep); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ParseEndpoint implements conn.Bind.
func (b *Bind) ParseEndpoint(s string) (conn.Endpoint, error) {
	if rest, ok := strings.CutPrefix(s, peerEndpointPrefix); ok {
		raw, err := hex.DecodeString(rest)
		if err != nil || len(raw) != 32 {
			return nil, errors.New("invalid peer endpoint")
		}
		var k identity.PublicKey
		copy(k[:], raw)
		return NewPeerEndpoint(k), nil
	}
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &RawEndpoint{Addr: ap}, nil
}

// BatchSize implements conn.Bind.
func (b *Bind) BatchSize() int { return 16 }
