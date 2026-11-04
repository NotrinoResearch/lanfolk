package tunwrap

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn/bindtest"
	"golang.zx2c4.com/wireguard/device"

	"github.com/NotrinoResearch/lanfolk/internal/winnet"
)

// genKey returns a clamped X25519 private key and its public key.
func genKey(t *testing.T) (priv, pub [32]byte) {
	t.Helper()
	if _, err := rand.Read(priv[:]); err != nil {
		t.Fatal(err)
	}
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	p, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	copy(pub[:], p)
	return priv, pub
}

type wgNode struct {
	env *testEnv
	dev *device.Device
}

// newWGPair connects two real wireguard-go devices over in-memory binds. Node
// 0 is 10.77.0.2 and node 1 is 10.77.0.3. blocked[i] is the denylist of node i.
func newWGPair(t *testing.T, blocked [2]func(uint8, uint16) bool) [2]*wgNode {
	t.Helper()
	privA, pubA := genKey(t)
	privB, pubB := genKey(t)
	privs := [2][32]byte{privA, privB}
	pubs := [2][32]byte{pubA, pubB}
	ips := [2]netip.Addr{addrSelf, addrB}
	binds := bindtest.NewChannelBinds()

	var nodes [2]*wgNode
	for i := 0; i < 2; i++ {
		i := i
		mem := NewMemTUN(fmt.Sprintf("wg%d", i), 1380)
		peer := ips[i^1]
		env := newEnvMem(t, mem, func(c *Config) {
			c.Blocked = blocked[i]
			c.Self = func() netip.Addr { return ips[i] }
			c.Peers = func() []netip.Addr { return []netip.Addr{peer} }
			c.Prefix = testPrefix
		})
		env.d.now = time.Now // real clock: the fake one never advances
		dev := device.NewDevice(env.d, binds[i], device.NewLogger(device.LogLevelError, fmt.Sprintf("wg%d: ", i)))
		t.Cleanup(dev.Close)
		cfg := fmt.Sprintf("private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\n",
			hex.EncodeToString(privs[i][:]), hex.EncodeToString(pubs[i^1][:]), ips[i^1])
		if err := dev.IpcSet(cfg); err != nil {
			t.Fatalf("IpcSet %d: %v", i, err)
		}
		if err := dev.Up(); err != nil {
			t.Fatalf("Up %d: %v", i, err)
		}
		nodes[i] = &wgNode{env: env, dev: dev}
	}
	// Point each peer at the other device's listen port.
	for i := 0; i < 2; i++ {
		get, err := nodes[i^1].dev.IpcGet()
		if err != nil {
			t.Fatal(err)
		}
		var port string
		for _, line := range strings.Split(get, "\n") {
			if v, ok := strings.CutPrefix(line, "listen_port="); ok {
				port = v
			}
		}
		cfg := fmt.Sprintf("public_key=%s\nendpoint=127.0.0.1:%s\n", hex.EncodeToString(pubs[i^1][:]), port)
		if err := nodes[i].dev.IpcSet(cfg); err != nil {
			t.Fatalf("endpoint %d: %v", i, err)
		}
	}
	return nodes
}

// waitOS waits for one packet delivered to the OS of the node.
func waitOS(t *testing.T, n *wgNode, timeout time.Duration) ([]byte, bool) {
	t.Helper()
	select {
	case p := <-n.env.mem.ToOS():
		return p, true
	case <-time.After(timeout):
		return nil, false
	}
}

func TestIntegrationBroadcastOverWireGuard(t *testing.T) {
	for _, dst := range []netip.Addr{addrBcast, addrGlobal, addrMcast} {
		t.Run(dst.String(), func(t *testing.T) {
			nodes := newWGPair(t, [2]func(uint8, uint16) bool{winnet.IsBlocked, winnet.IsBlocked})
			a, b := nodes[0], nodes[1]
			pkt := udpPkt(addrSelf, dst, 4000, 4001, "anyone there?")
			a.env.mem.InjectFromOS(pkt)
			got, ok := waitOS(t, b, 10*time.Second)
			if !ok {
				t.Fatalf("broadcast did not arrive; A stats %+v", a.env.d.Stats())
			}
			if !bytes.Equal(got, pkt) {
				t.Fatalf("delivered packet differs:\n got %x\nwant %x", got, pkt)
			}
			src, d, payload := mustUDP(t, got)
			if src.Addr() != addrSelf || d.Addr() != dst || string(payload) != "anyone there?" {
				t.Fatalf("got %v -> %v %q", src, d, payload)
			}
			if s := a.env.d.Stats(); s.LanFramesOut != 1 {
				t.Fatalf("sender stats %+v", s)
			}
			if s := b.env.d.Stats(); s.LanFramesIn != 1 {
				t.Fatalf("receiver stats %+v", s)
			}
		})
	}
}

func TestIntegrationCapturedBroadcastOverWireGuard(t *testing.T) {
	nodes := newWGPair(t, [2]func(uint8, uint16) bool{winnet.IsBlocked, winnet.IsBlocked})
	a, b := nodes[0], nodes[1]
	// A capture from loopback has a foreign source; B must see A's virtual IP.
	a.env.d.InjectLocalBroadcast(udpPkt(netip.MustParseAddr("127.0.0.1"), addrGlobal, 7777, 7778, "captured"))
	got, ok := waitOS(t, b, 10*time.Second)
	if !ok {
		t.Fatal("captured broadcast did not arrive")
	}
	src, dst, payload := mustUDP(t, got)
	if src.Addr() != addrSelf || src.Port() != 7777 || dst.Addr() != addrGlobal || dst.Port() != 7778 || string(payload) != "captured" {
		t.Fatalf("got %v -> %v %q", src, dst, payload)
	}
}

func TestIntegrationUnicast(t *testing.T) {
	nodes := newWGPair(t, [2]func(uint8, uint16) bool{winnet.IsBlocked, winnet.IsBlocked})
	a, b := nodes[0], nodes[1]

	// A unicast to B's SMB port never arrives; the sender drops it.
	a.env.mem.InjectFromOS(rawTCP(addrSelf, addrB, 50000, 445))
	// A normal unicast arrives unchanged, and it is the first thing to arrive.
	pkt := udpPkt(addrSelf, addrB, 5000, 27015, "game traffic")
	a.env.mem.InjectFromOS(pkt)
	got, ok := waitOS(t, b, 10*time.Second)
	if !ok {
		t.Fatal("unicast did not arrive")
	}
	if !bytes.Equal(got, pkt) {
		t.Fatalf("unicast changed:\n got %x\nwant %x", got, pkt)
	}
	if s := a.env.d.Stats(); s.DroppedBlocked != 1 {
		t.Fatalf("sender stats %+v", s)
	}
	if p, ok := waitOS(t, b, 300*time.Millisecond); ok {
		t.Fatalf("unexpected extra packet %x", p)
	}

	// And the other way round.
	reply := udpPkt(addrB, addrSelf, 27015, 5000, "reply")
	b.env.mem.InjectFromOS(reply)
	got, ok = waitOS(t, a, 10*time.Second)
	if !ok || !bytes.Equal(got, reply) {
		t.Fatalf("reply did not arrive intact: %v %x", ok, got)
	}
}

func TestIntegrationReceiverEnforcesDenylist(t *testing.T) {
	// The sender has no denylist; the receiver must still drop the packet.
	nodes := newWGPair(t, [2]func(uint8, uint16) bool{nil, winnet.IsBlocked})
	a, b := nodes[0], nodes[1]
	a.env.mem.InjectFromOS(rawTCP(addrSelf, addrB, 50000, 3389))
	a.env.mem.InjectFromOS(udpPkt(addrSelf, addrB, 1, 137, "netbios"))
	pkt := udpPkt(addrSelf, addrB, 5000, 27015, "sentinel")
	a.env.mem.InjectFromOS(pkt)
	got, ok := waitOS(t, b, 10*time.Second)
	if !ok || !bytes.Equal(got, pkt) {
		t.Fatalf("expected only the sentinel, got %v %x", ok, got)
	}
	if s := b.env.d.Stats(); s.DroppedBlocked != 2 {
		t.Fatalf("receiver stats %+v", s)
	}
}
