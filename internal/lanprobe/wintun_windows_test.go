//go:build windows

package lanprobe

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// fakeDevice is a tun.Device backed by channels: what the test "sends from an
// application" is returned by Read, and what the code under test writes can
// be inspected.
type fakeDevice struct {
	toRead  chan []byte
	written chan []byte
	closed  chan struct{}
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{toRead: make(chan []byte, 16), written: make(chan []byte, 16), closed: make(chan struct{})}
}

func (f *fakeDevice) File() *os.File { return nil }
func (f *fakeDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case p := <-f.toRead:
		sizes[0] = copy(bufs[0][offset:], p)
		return 1, nil
	case <-f.closed:
		return 0, os.ErrClosed
	}
}
func (f *fakeDevice) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		f.written <- append([]byte(nil), b[offset:]...)
	}
	return len(bufs), nil
}
func (f *fakeDevice) MTU() (int, error)        { return 1380, nil }
func (f *fakeDevice) Name() (string, error)    { return "fake", nil }
func (f *fakeDevice) Events() <-chan tun.Event { return nil }
func (f *fakeDevice) BatchSize() int           { return 1 }
func (f *fakeDevice) Close() error             { close(f.closed); return nil }

func TestAdapterTapFiltersAndMatches(t *testing.T) {
	dev := newFakeDevice()
	tap := newAdapterTap(dev)
	defer tap.close()

	// Background noise: an IPv6 packet, an ICMP packet, and a UDP packet
	// with somebody else's payload must be ignored.
	dev.toRead <- []byte{0x60, 0, 0, 0, 0, 0, 0x3a, 0xff}
	icmp := BuildIPv4UDP(stLocal, stSubnet, 1, 2, []byte("x"))
	icmp[9] = 1
	dev.toRead <- icmp
	dev.toRead <- BuildIPv4UDP(stLocal, stSubnet, 5353, 5353, []byte("mdns noise"))
	want := Probe{Token: "tok", Name: "a-any", Claimed: "10.77.0.2"}.Encode()
	dev.toRead <- BuildIPv4UDP(stLocal, stSubnet, 4000, 47800, want)

	p, ok := tap.waitFor("tok", "a-any", time.Second)
	if !ok {
		t.Fatal("probe not found")
	}
	if p.Src != stLocal || p.Dst != stSubnet || p.DstPort != 47800 || !bytes.Equal(p.Payload, want) {
		t.Fatalf("packet %+v", p)
	}
	if _, ok := tap.waitFor("tok", "other", 100*time.Millisecond); ok {
		t.Fatal("a different probe name must not match")
	}
}

func TestAdapterTapInject(t *testing.T) {
	dev := newFakeDevice()
	tap := newAdapterTap(dev)
	defer tap.close()
	if err := tap.inject(stRemote, stGroup, 40000, 47830, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	pkt := <-dev.written
	u, err := ParseIPv4UDP(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if u.Src != stRemote || u.Dst != stGroup || u.DstPort != 47830 || string(u.Payload) != "hello" || !u.IPChecksumOK || !u.UDPChecksumOK {
		t.Fatalf("injected packet %+v", u)
	}
}

func TestSelfTestNotElevatedOrNoDLL(t *testing.T) {
	// The test process is normally not elevated; then the self-test must
	// exit with code 2 and explain why. When it is elevated, the missing
	// wintun.dll next to the test binary gives the same code.
	var out strings.Builder
	code := RunWintunSelfTest(context.Background(), &out, SelfTestOptions{})
	if code != 2 {
		t.Skipf("environment has administrator rights and wintun.dll (exit %d): %s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "administrator") && !strings.Contains(s, "wintun.dll") {
		t.Errorf("message does not explain the problem: %q", s)
	}
	if strings.Contains(s, "wintun.dll") && !strings.Contains(s, WintunURL) {
		t.Errorf("missing download URL: %q", s)
	}
}
