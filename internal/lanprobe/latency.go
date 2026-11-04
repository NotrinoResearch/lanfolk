package lanprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
)

// Tunnel overhead targets from the implementation plan (direct path).
const (
	TargetOverheadP50 = 1 * time.Millisecond
	TargetOverheadP99 = 3 * time.Millisecond
)

const latencyMagic = "LFLAT001"

// minLatencySize is the smallest datagram: magic + sequence number.
const minLatencySize = len(latencyMagic) + 4

// LatencyConfig configures one UDP echo latency run.
type LatencyConfig struct {
	Peer  string  // IPv4 address of the echo server
	Port  int     // default DefaultPorts.Latency
	Count int     // packets to send, default 10000
	Size  int     // datagram size in bytes, default 200
	Rate  float64 // packets per second, default 64
	// Linger is how long to wait for the last replies. Default 1 s.
	Linger time.Duration
	// Progress, if set, is called about once a second with the packets sent.
	Progress func(sent int)
}

func (c *LatencyConfig) defaults() error {
	if c.Port == 0 {
		c.Port = DefaultPorts.Latency
	}
	if c.Count <= 0 {
		c.Count = 10000
	}
	if c.Size == 0 {
		c.Size = 200
	}
	if c.Size < minLatencySize || c.Size > 1472 {
		return fmt.Errorf("-size must be between %d and 1472", minLatencySize)
	}
	if c.Rate <= 0 {
		c.Rate = 64
	}
	if c.Linger <= 0 {
		c.Linger = time.Second
	}
	return nil
}

// RunLatency sends Count paced UDP packets to the echo port of the peer and
// measures each round trip with the high-resolution clock.
func RunLatency(ctx context.Context, cfg LatencyConfig) (Summary, error) {
	if err := cfg.defaults(); err != nil {
		return Summary{}, err
	}
	peer, err := netip.ParseAddr(cfg.Peer)
	if err != nil || !peer.Is4() {
		return Summary{}, fmt.Errorf("peer must be an IPv4 address, got %q", cfg.Peer)
	}
	conn, err := listenUDP4("0.0.0.0:0")
	if err != nil {
		return Summary{}, err
	}
	defer conn.Close()
	conn.SetReadBuffer(1 << 20)
	dst := netip.AddrPortFrom(peer, uint16(cfg.Port))

	sentAt := make([]atomic.Int64, cfg.Count)
	rtt := make([]atomic.Int64, cfg.Count)
	var received atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		errs := 0
		for {
			n, src, err := conn.ReadFromUDPAddrPort(buf)
			now := clock.Nanotime()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				// Windows reports ICMP port unreachable as a read error;
				// keep going, but do not spin forever on a broken socket.
				if errs++; errs > 100000 {
					return
				}
				continue
			}
			if n < minLatencySize || string(buf[:len(latencyMagic)]) != latencyMagic || src.Addr().Unmap() != peer {
				continue
			}
			seq := int(binary.BigEndian.Uint32(buf[len(latencyMagic):]))
			if seq < 0 || seq >= cfg.Count {
				continue
			}
			t0 := sentAt[seq].Load()
			if t0 == 0 {
				continue
			}
			d := now - t0
			if d < 1 {
				d = 1
			}
			if rtt[seq].CompareAndSwap(0, d) {
				received.Add(1)
			}
		}
	}()

	interval := time.Duration(float64(time.Second) / cfg.Rate)
	pkt := make([]byte, cfg.Size)
	copy(pkt, latencyMagic)
	start := clock.Nanotime()
	lastProgress := start
	sent := 0
loop:
	for i := 0; i < cfg.Count; i++ {
		if wait := time.Duration(start + int64(i)*int64(interval) - clock.Nanotime()); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				break loop
			case <-t.C:
			}
		} else if ctx.Err() != nil {
			break
		}
		binary.BigEndian.PutUint32(pkt[len(latencyMagic):], uint32(i))
		now := clock.Nanotime()
		sentAt[i].Store(now)
		if _, err := conn.WriteToUDPAddrPort(pkt, dst); err != nil {
			// Count the packet as lost and carry on (e.g. transient no buffer).
			sent++
			continue
		}
		sent++
		if cfg.Progress != nil && now-lastProgress >= int64(time.Second) {
			lastProgress = now
			cfg.Progress(sent)
		}
	}
	// Wait for stragglers, but stop early once every reply has arrived.
	deadline := time.Now().Add(cfg.Linger)
	for time.Now().Before(deadline) && int(received.Load()) < sent && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	conn.Close()
	<-done

	rtts := make([]time.Duration, 0, sent)
	for i := 0; i < sent; i++ {
		if d := rtt[i].Load(); d > 0 {
			rtts = append(rtts, time.Duration(d))
		}
	}
	s := Summarize(rtts, sent)
	if sent == 0 {
		return s, errors.New("interrupted before any packet was sent")
	}
	return s, nil
}

// RunLatencyCompare measures the raw path and the tunneled path at the same
// time, so both see the same network conditions. The second run starts half a
// packet interval after the first so their packets do not coincide.
func RunLatencyCompare(ctx context.Context, raw, tunnel LatencyConfig) (rawSum, tunSum Summary, err error) {
	if err := tunnel.defaults(); err != nil {
		return rawSum, tunSum, err
	}
	half := time.Duration(float64(time.Second) / tunnel.Rate / 2)
	type out struct {
		s   Summary
		err error
	}
	rc := make(chan out, 1)
	go func() {
		s, err := RunLatency(ctx, raw)
		rc <- out{s, err}
	}()
	time.Sleep(half)
	tunSum, terr := RunLatency(ctx, tunnel)
	r := <-rc
	if r.err != nil {
		return r.s, tunSum, fmt.Errorf("raw path: %w", r.err)
	}
	if terr != nil {
		return r.s, tunSum, fmt.Errorf("tunnel path: %w", terr)
	}
	return r.s, tunSum, nil
}

// Overhead is the difference between a tunneled and a raw path.
type Overhead struct {
	P50, P99 time.Duration
	OKP50    bool
	OKP99    bool
}

// ComputeOverhead compares a tunneled run with a raw run against the plan
// targets (at most 1 ms at p50 and 3 ms at p99).
func ComputeOverhead(raw, tunnel Summary) Overhead {
	o := Overhead{P50: tunnel.P50 - raw.P50, P99: tunnel.P99 - raw.P99}
	o.OKP50 = o.P50 <= TargetOverheadP50
	o.OKP99 = o.P99 <= TargetOverheadP99
	return o
}

// WriteSummary prints one latency summary.
func WriteSummary(w io.Writer, label string, s Summary) {
	fmt.Fprintf(w, "%-8s sent %d, received %d, loss %.2f%%\n", label, s.Sent, s.Received, s.LossPct)
	fmt.Fprintf(w, "%-8s min %s  p50 %s  p95 %s  p99 %s  max %s  mean %s\n", "", Ms(s.Min), Ms(s.P50), Ms(s.P95), Ms(s.P99), Ms(s.Max), Ms(s.Mean))
}

// WriteOverhead prints the overhead against the targets.
func WriteOverhead(w io.Writer, o Overhead) {
	verdict := func(ok bool) string {
		if ok {
			return "PASS"
		}
		return "FAIL"
	}
	fmt.Fprintf(w, "Tunnel overhead p50 %+.3f ms (target <= %.0f ms): %s\n", ms(o.P50), ms(TargetOverheadP50), verdict(o.OKP50))
	fmt.Fprintf(w, "Tunnel overhead p99 %+.3f ms (target <= %.0f ms): %s\n", ms(o.P99), ms(TargetOverheadP99), verdict(o.OKP99))
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
