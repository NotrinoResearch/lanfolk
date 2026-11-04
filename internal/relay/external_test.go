package relay

import (
	"crypto/rand"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/stun"
	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// TestExternalRelay checks a deployed relay over the internet. It runs only
// when LANFOLK_RELAY_TEST_HOST is set, for example:
//
//	LANFOLK_RELAY_TEST_HOST=relay.example.org LANFOLK_RELAY_TEST_TCP_PORT=8443 go test ./internal/relay -run External -v
//
// The host is never printed so that test output can be shared.
func TestExternalRelay(t *testing.T) {
	host := os.Getenv("LANFOLK_RELAY_TEST_HOST")
	if host == "" {
		t.Skip("LANFOLK_RELAY_TEST_HOST not set")
	}
	udpPort := envPort("LANFOLK_RELAY_TEST_UDP_PORT", 3478)
	tcpPort := envPort("LANFOLK_RELAY_TEST_TCP_PORT", 443)
	ua, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(udpPort)))
	if err != nil {
		t.Fatal("resolve failed")
	}
	srv := ua.AddrPort()

	a := dialUDP(t)
	b := dialUDP(t)

	t.Run("stun", func(t *testing.T) {
		tx := stun.NewTxID()
		start := time.Now()
		a.WriteToUDPAddrPort(stun.BindingRequest(tx), srv)
		resp := readUDP(t, a, 3*time.Second)
		rtx, mapped, err := stun.ParseBindingResponse(resp)
		if err != nil || rtx != tx {
			t.Fatalf("no STUN answer: %v", err)
		}
		t.Logf("STUN ok, rtt %v, mapped address is public: %v", time.Since(start).Round(time.Millisecond),
			!mapped.Addr().IsPrivate())
	})

	var sid wire.SessionID
	rand.Read(sid[:])
	t.Run("udp-forward", func(t *testing.T) {
		a.WriteToUDPAddrPort(wire.AppendRegister(nil, sid, wire.Token{1}), srv)
		expectControl(t, a, wire.RelayRegisterAck)
		b.WriteToUDPAddrPort(wire.AppendRegister(nil, sid, wire.Token{2}), srv)
		expectControl(t, b, wire.RelayRegisterAck)
		expectControl(t, a, wire.RelayPeerJoined)
		var rtts []time.Duration
		for i := 0; i < 20; i++ {
			start := time.Now()
			a.WriteToUDPAddrPort(wire.AppendRelayData(nil, sid, []byte{byte(i)}), srv)
			got := readUDP(t, b, 2*time.Second)
			if _, p, ok := wire.ParseRelayData(got); !ok || len(p) != 1 || p[0] != byte(i) {
				t.Fatalf("packet %d not forwarded", i)
			}
			b.WriteToUDPAddrPort(wire.AppendRelayData(nil, sid, []byte{byte(i)}), srv)
			readUDP(t, a, 2*time.Second)
			rtts = append(rtts, time.Since(start))
		}
		t.Logf("UDP relay round trip (a->relay->b->relay->a) min %v", minDur(rtts))
	})

	t.Run("tcp-forward", func(t *testing.T) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(tcpPort)), 5*time.Second)
		if err != nil {
			t.Fatal("TCP fallback port unreachable")
		}
		defer c.Close()
		var sid2 wire.SessionID
		rand.Read(sid2[:])
		a.WriteToUDPAddrPort(wire.AppendRegister(nil, sid2, wire.Token{1}), srv)
		expectControl(t, a, wire.RelayRegisterAck)
		c.Write(wire.AppendStreamFrame(nil, wire.AppendRegister(nil, sid2, wire.Token{2})))
		buf := make([]byte, 4096)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		f, err := wire.ReadStreamFrame(c, buf)
		if err != nil {
			t.Fatal(err)
		}
		if m, err := wire.ParseRelayControl(f); err != nil || !m.PeerPresent {
			t.Fatalf("tcp register: %+v %v", m, err)
		}
		expectControl(t, a, wire.RelayPeerJoined)
		start := time.Now()
		c.Write(wire.AppendStreamFrame(nil, wire.AppendRelayData(nil, sid2, []byte("t"))))
		if _, p, ok := wire.ParseRelayData(readUDP(t, a, 3*time.Second)); !ok || string(p) != "t" {
			t.Fatal("tcp->udp not forwarded")
		}
		a.WriteToUDPAddrPort(wire.AppendRelayData(nil, sid2, []byte("u")), srv)
		f, err = wire.ReadStreamFrame(c, buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, p, ok := wire.ParseRelayData(f); !ok || string(p) != "u" {
			t.Fatal("udp->tcp not forwarded")
		}
		t.Logf("TCP<->UDP relay round trip %v", time.Since(start).Round(time.Millisecond))
	})
}

func envPort(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func dialUDP(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readUDP(t *testing.T, c *net.UDPConn, d time.Duration) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 2048)
	n, _, err := c.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatal("timeout waiting for relay")
	}
	return buf[:n]
}

func expectControl(t *testing.T, c *net.UDPConn, typ byte) wire.RelayControl {
	t.Helper()
	for i := 0; i < 5; i++ {
		m, err := wire.ParseRelayControl(readUDP(t, c, 3*time.Second))
		if err == nil && m.Type == typ {
			return m
		}
	}
	t.Fatalf("expected relay message %d", typ)
	return wire.RelayControl{}
}

func minDur(d []time.Duration) time.Duration {
	m := d[0]
	for _, v := range d {
		if v < m {
			m = v
		}
	}
	return m.Round(100 * time.Microsecond)
}
