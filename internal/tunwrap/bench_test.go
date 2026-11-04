package tunwrap

import (
	"net/netip"
	"os"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/NotrinoResearch/lanfolk/internal/winnet"
)

// loopTUN is an inner device whose Read returns a fixed packet instantly and
// whose Write discards, so benchmarks measure only the wrapper.
type loopTUN struct {
	pkt    []byte
	closed chan struct{}
	once   sync.Once
	events chan tun.Event
}

func newLoopTUN(pkt []byte) *loopTUN {
	return &loopTUN{pkt: pkt, closed: make(chan struct{}), events: make(chan tun.Event)}
}

func (l *loopTUN) File() *os.File { return nil }
func (l *loopTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-l.closed:
		return 0, os.ErrClosed
	default:
	}
	sizes[0] = copy(bufs[0][offset:], l.pkt)
	return 1, nil
}
func (l *loopTUN) Write(bufs [][]byte, offset int) (int, error) { return len(bufs), nil }
func (l *loopTUN) MTU() (int, error)                            { return 1380, nil }
func (l *loopTUN) Name() (string, error)                        { return "loop", nil }
func (l *loopTUN) Events() <-chan tun.Event                     { return l.events }
func (l *loopTUN) BatchSize() int                               { return 1 }
func (l *loopTUN) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func benchDevice(tb testing.TB, pkt []byte) *Device {
	tb.Helper()
	d := Wrap(newLoopTUN(pkt), Config{
		Self:    func() netip.Addr { return addrSelf },
		Peers:   func() []netip.Addr { return []netip.Addr{addrB} },
		Blocked: winnet.IsBlocked,
		Prefix:  testPrefix,
	})
	tb.Cleanup(func() { _ = d.Close() })
	return d
}

func BenchmarkUnicastRead(b *testing.B) {
	pkt := udpPkt(addrSelf, addrB, 5000, 27015, string(make([]byte, 64)))
	d := benchDevice(b, pkt)
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	b.SetBytes(int64(len(pkt)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n, err := d.Read(bufs, sizes, 16); n != 1 || err != nil {
			b.Fatalf("Read = %d, %v", n, err)
		}
	}
}

func BenchmarkUnicastWrite(b *testing.B) {
	pkt := udpPkt(addrB, addrSelf, 27015, 5000, string(make([]byte, 64)))
	d := benchDevice(b, pkt)
	buf := make([]byte, 16+len(pkt))
	copy(buf[16:], pkt)
	bufs := [][]byte{buf}
	b.SetBytes(int64(len(pkt)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n, err := d.Write(bufs, 16); n != 1 || err != nil {
			b.Fatalf("Write = %d, %v", n, err)
		}
	}
}

func BenchmarkFanOutRead(b *testing.B) {
	pkt := udpPkt(addrSelf, addrBcast, 5000, 27015, string(make([]byte, 64)))
	d := benchDevice(b, pkt)
	d.limiter.rate = 1 << 30
	d.limiter.burst = 1 << 62
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	b.SetBytes(int64(len(pkt)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n, err := d.Read(bufs, sizes, 16); n != 1 || err != nil {
			b.Fatalf("Read = %d, %v", n, err)
		}
	}
}

func BenchmarkLanFrameWrite(b *testing.B) {
	inner := udpPkt(addrB, addrGlobal, 5000, 27015, string(make([]byte, 64)))
	frame := lanFrame(addrB, addrSelf, inner)
	d := benchDevice(b, inner)
	buf := make([]byte, 16+len(frame))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(buf[16:], frame) // the wrapper unwraps in place
		if n, err := d.Write([][]byte{buf}, 16); n != 1 || err != nil {
			b.Fatalf("Write = %d, %v", n, err)
		}
	}
}

// TestUnicastPathDoesNotAllocate guards the allocation discipline.
func TestUnicastPathDoesNotAllocate(t *testing.T) {
	out := udpPkt(addrSelf, addrB, 5000, 27015, "payload")
	in := udpPkt(addrB, addrSelf, 27015, 5000, "payload")
	d := benchDevice(t, out)
	rbufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	wbuf := make([]byte, 16+len(in))
	copy(wbuf[16:], in)
	wbufs := [][]byte{wbuf}
	if a := testing.AllocsPerRun(200, func() {
		if n, err := d.Write(wbufs, 16); n != 1 || err != nil {
			t.Fatal("Write failed")
		}
	}); a != 0 {
		t.Errorf("Write allocates %v times per packet", a)
	}
	if a := testing.AllocsPerRun(200, func() {
		if n, err := d.Read(rbufs, sizes, 16); n != 1 || err != nil {
			t.Fatal("Read failed")
		}
	}); a > 0 {
		t.Errorf("Read allocates %v times per packet", a)
	}
}
