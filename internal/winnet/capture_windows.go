//go:build windows

package winnet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// sioRcvAll is SIO_RCVALL = _WSAIOW(IOC_VENDOR, 1).
	sioRcvAll = 0x98000001
	// rcvAllOn is RCVALL_ON.
	rcvAllOn = 1

	captureRecvBuf   = 1 << 20
	maxConsecutiveRx = 50
)

var wsaOnce sync.Once
var wsaErr error

func startWinsock() error {
	wsaOnce.Do(func() {
		var d windows.WSAData
		wsaErr = windows.WSAStartup(0x0202, &d)
	})
	return wsaErr
}

// BroadcastCapture delivers locally sent 255.255.255.255 datagrams.
type BroadcastCapture struct {
	sock   windows.Handle
	cache  *localAddrCache
	cb     func([]byte)
	once   sync.Once
	closed chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// StartBroadcastCapture opens a raw IPv4 socket bound to 127.0.0.1 and calls
// onPacket for every captured global broadcast: an IPv4 UDP packet (IP header
// included) to 255.255.255.255 sent from one of this machine's addresses
// outside 10.77.0.0/24. See the limitation described in capture.go: packets
// whose source and destination ports are identical are skipped.
//
// onPacket runs on the capture goroutine and gets its own copy of the packet.
// Capture ends when ctx is cancelled or Close is called. It needs
// administrator rights and returns ErrNotElevated otherwise.
func StartBroadcastCapture(ctx context.Context, onPacket func(ipv4Packet []byte)) (*BroadcastCapture, error) {
	if onPacket == nil {
		return nil, errors.New("winnet: nil packet callback")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil, ErrNotElevated
	}
	if err := startWinsock(); err != nil {
		return nil, fmt.Errorf("winnet: WSAStartup: %w", err)
	}
	s, err := windows.Socket(windows.AF_INET, windows.SOCK_RAW, windows.IPPROTO_UDP)
	if err != nil {
		if errors.Is(err, windows.WSAEACCES) {
			return nil, ErrNotElevated
		}
		return nil, fmt.Errorf("winnet: raw socket: %w", err)
	}
	fail := func(what string, err error) (*BroadcastCapture, error) {
		windows.Closesocket(s)
		return nil, fmt.Errorf("winnet: %s: %w", what, err)
	}
	if err := windows.Bind(s, &windows.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return fail("bind 127.0.0.1", err)
	}
	// A large receive buffer absorbs bursts. Failing to set it is harmless.
	_ = windows.SetsockoptInt(s, windows.SOL_SOCKET, windows.SO_RCVBUF, captureRecvBuf)
	// Without RCVALL a raw socket sees only packets addressed to its bound
	// address, not the broadcast copy on the loopback path.
	on := uint32(rcvAllOn)
	var ret uint32
	if err := windows.WSAIoctl(s, sioRcvAll, (*byte)(unsafe.Pointer(&on)), uint32(unsafe.Sizeof(on)),
		nil, 0, &ret, nil, 0); err != nil {
		return fail("SIO_RCVALL", err)
	}

	c := &BroadcastCapture{
		sock:   s,
		cache:  newLocalAddrCache(),
		cb:     onPacket,
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go c.loop()
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-c.done:
		}
	}()
	return c, nil
}

func (c *BroadcastCapture) loop() {
	defer close(c.done)
	buf := make([]byte, 65536)
	failures := 0
	for {
		n, _, err := windows.Recvfrom(c.sock, buf, 0)
		select {
		case <-c.closed:
			return
		default:
		}
		if err != nil {
			// WSAEMSGSIZE cannot happen with a 64 KiB buffer; treat every
			// error as transient until it repeats.
			failures++
			if failures >= maxConsecutiveRx {
				c.setErr(fmt.Errorf("winnet: capture receive: %w", err))
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		failures = 0
		pkt, reason := filterBroadcast(buf[:n], c.cache)
		if reason != rejectNone {
			continue
		}
		c.cb(append([]byte(nil), pkt...))
	}
}

func (c *BroadcastCapture) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// Err returns the error that stopped the capture, if it stopped by itself.
func (c *BroadcastCapture) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Done is closed after the capture goroutine has exited.
func (c *BroadcastCapture) Done() <-chan struct{} { return c.done }

// Close stops the capture and waits for the goroutine to exit. It is safe to
// call more than once.
func (c *BroadcastCapture) Close() error {
	var err error
	c.once.Do(func() {
		close(c.closed)
		err = windows.Closesocket(c.sock)
	})
	<-c.done
	return err
}
