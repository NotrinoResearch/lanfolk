package tunwrap

import (
	"os"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// MemTUN is an in-memory tun.Device for tests. Packets the "OS" sends into the
// adapter are queued with InjectFromOS and returned by Read; packets written
// to the adapter are delivered on the ToOS channel.
type MemTUN struct {
	name  string
	mtu   int
	batch int

	in     chan []byte
	out    chan []byte
	events chan tun.Event

	closed     chan struct{}
	closeOnce  sync.Once
	writeCalls atomic.Int64
}

var _ tun.Device = (*MemTUN)(nil)

const memQueue = 4096

// NewMemTUN returns an open in-memory device with the given name and MTU.
func NewMemTUN(name string, mtu int) *MemTUN {
	return &MemTUN{
		name:   name,
		mtu:    mtu,
		batch:  1,
		in:     make(chan []byte, memQueue),
		out:    make(chan []byte, memQueue),
		events: make(chan tun.Event),
		closed: make(chan struct{}),
	}
}

// WithBatchSize sets the batch size reported by BatchSize. Call it before the
// device is used; BatchSize must not change over the lifetime of a device.
func (m *MemTUN) WithBatchSize(n int) *MemTUN {
	if n < 1 {
		n = 1
	}
	m.batch = n
	return m
}

// InjectFromOS queues a packet the operating system sends into the adapter.
// The packet is copied. It blocks only if the queue is full and returns
// immediately once the device is closed.
func (m *MemTUN) InjectFromOS(pkt []byte) {
	c := append(make([]byte, 0, len(pkt)), pkt...)
	select {
	case m.in <- c:
	case <-m.closed:
	}
}

// ToOS returns the channel of packets written to the adapter, that is,
// packets delivered to the operating system.
func (m *MemTUN) ToOS() <-chan []byte { return m.out }

// WriteCalls returns how many times Write has been called.
func (m *MemTUN) WriteCalls() int { return int(m.writeCalls.Load()) }

// File implements tun.Device. An in-memory device has no file.
func (m *MemTUN) File() *os.File { return nil }

// Read implements tun.Device. It blocks until at least one packet is
// available, then returns as many queued packets as fit.
func (m *MemTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if len(bufs) == 0 {
		return 0, nil
	}
	limit := min(len(bufs), m.batch)
	n := 0
	put := func(p []byte) {
		if offset+len(p) > len(bufs[n]) {
			return // does not fit: dropped, like a truncated read
		}
		sizes[n] = copy(bufs[n][offset:], p)
		n++
	}
	select {
	case <-m.closed:
		return 0, os.ErrClosed
	case p := <-m.in:
		put(p)
	}
	for n < limit {
		select {
		case p := <-m.in:
			put(p)
		default:
			return n, nil
		}
	}
	return n, nil
}

// Write implements tun.Device. Each packet is copied to the ToOS channel.
func (m *MemTUN) Write(bufs [][]byte, offset int) (int, error) {
	m.writeCalls.Add(1)
	select {
	case <-m.closed:
		return 0, os.ErrClosed
	default:
	}
	for i, b := range bufs {
		c := append(make([]byte, 0, len(b)-offset), b[offset:]...)
		select {
		case m.out <- c:
		case <-m.closed:
			return i, os.ErrClosed
		}
	}
	return len(bufs), nil
}

// MTU implements tun.Device.
func (m *MemTUN) MTU() (int, error) { return m.mtu, nil }

// Name implements tun.Device.
func (m *MemTUN) Name() (string, error) { return m.name, nil }

// Events implements tun.Device. No events are ever sent.
func (m *MemTUN) Events() <-chan tun.Event { return m.events }

// BatchSize implements tun.Device.
func (m *MemTUN) BatchSize() int { return m.batch }

// Close implements tun.Device. It is idempotent.
func (m *MemTUN) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
		close(m.events)
	})
	return nil
}
