package relay

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// TestRelayLoad drives many game-like sessions through an in-process relay:
// LANFOLK_LOADTEST_PAIRS session pairs (default 200), each side sending
// 64 packets/s of 200 bytes for LANFOLK_LOADTEST_SECONDS (default 10). It
// reports delivery and relay latency. Run with LANFOLK_LOADTEST=1.
func TestRelayLoad(t *testing.T) {
	if os.Getenv("LANFOLK_LOADTEST") == "" {
		t.Skip("set LANFOLK_LOADTEST=1 to load test the relay")
	}
	pairs := envInt("LANFOLK_LOADTEST_PAIRS", 200)
	readers := envInt("LANFOLK_LOADTEST_READERS", 1)
	seconds := envInt("LANFOLK_LOADTEST_SECONDS", 10)
	const rate, size = 64, 200
	// All load clients share 127.0.0.1, so lift the per-IP control limit.
	// The same goes for the per-IP data cap.
	s := startServer(t, func(c *Config) {
		c.MaxSessionsPerIP = 2 * pairs
		c.ControlPerIPRate = 1e6
		c.MaxIPKbps = 0
		c.UDPReaders = readers
	})
	srv := s.UDPAddr()

	type side struct {
		conn *net.UDPConn
		sid  wire.SessionID
	}
	var sides []side
	for i := 0; i < pairs; i++ {
		var sid wire.SessionID
		rand.Read(sid[:])
		for j := 0; j < 2; j++ {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			c.SetReadBuffer(1 << 20)
			defer c.Close()
			c.WriteToUDPAddrPort(wire.AppendRegister(nil, sid, wire.Token{byte(j + 1)}), srv)
			sides = append(sides, side{c, sid})
		}
	}
	time.Sleep(500 * time.Millisecond)

	var sent, recv atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, sd := range sides {
		sd := sd
		wg.Add(2)
		go func() { // receiver
			defer wg.Done()
			buf := make([]byte, 2048)
			var local []time.Duration
			for {
				sd.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				n, _, err := sd.conn.ReadFromUDPAddrPort(buf)
				if err != nil {
					select {
					case <-stop:
						mu.Lock()
						lat = append(lat, local...)
						mu.Unlock()
						return
					default:
						continue
					}
				}
				if _, p, ok := wire.ParseRelayData(buf[:n]); ok && len(p) >= 8 {
					recv.Add(1)
					local = append(local, clock.Since(int64(binary.BigEndian.Uint64(p))))
				}
			}
		}()
		go func() { // sender
			defer wg.Done()
			tk := time.NewTicker(time.Second / rate)
			defer tk.Stop()
			payload := make([]byte, size)
			frame := make([]byte, 0, size+wire.RelayDataOverhead)
			end := time.Now().Add(time.Duration(seconds) * time.Second)
			for time.Now().Before(end) {
				<-tk.C
				binary.BigEndian.PutUint64(payload, uint64(clock.Nanotime()))
				frame = wire.AppendRelayData(frame[:0], sd.sid, payload)
				sd.conn.WriteToUDPAddrPort(frame, srv)
				sent.Add(1)
			}
		}()
	}
	time.Sleep(time.Duration(seconds)*time.Second + time.Second)
	close(stop)
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	q := func(p int) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[(len(lat)-1)*p/100]
	}
	delivered := float64(recv.Load()) / float64(sent.Load())
	t.Logf("%d readers, %d sessions, %d pps offered (%.1f Mbit/s), delivered %.3f%%", readers, pairs, 2*pairs*rate,
		float64(2*pairs*rate*(size+wire.RelayDataOverhead)*8)/1e6, delivered*100)
	t.Logf("one-way relay latency p50 %v p99 %v max %v", q(50), q(99), q(100))
	t.Logf("relay drops: rate %d, no peer %d, unregistered %d", s.metrics.droppedRate.Load(),
		s.metrics.droppedNoPeer.Load(), s.metrics.droppedUnregistered.Load())
	if delivered < 0.999 {
		t.Errorf("delivery %.3f%% below 99.9%%", delivered*100)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
