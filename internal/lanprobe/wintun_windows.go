//go:build windows

package lanprobe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const (
	selfTestAdapter  = "LanfolkSelfTest"
	selfTestMTU      = 1380
	selfTestRuleName = "LanfolkSelfTest"
	// WintunURL is where wintun.dll comes from.
	WintunURL = "https://www.wintun.net/"
)

var (
	stLocal  = netip.MustParseAddr("10.77.0.2")
	stRemote = netip.MustParseAddr("10.77.0.3")
	stSubnet = netip.MustParseAddr("10.77.0.255")
	stGlobal = netip.MustParseAddr("255.255.255.255")
	stGroup  = netip.MustParseAddr("239.255.77.77")
	stPrefix = netip.MustParsePrefix("10.77.0.2/24")
)

// SelfTestOptions controls RunWintunSelfTest.
type SelfTestOptions struct {
	// NoFirewall skips the temporary inbound allow rule for this executable.
	// Without a rule, Windows Firewall may drop the packets injected in
	// tests (d) and (e).
	NoFirewall bool
}

// RunWintunSelfTest creates a temporary Wintun adapter and measures how
// Windows delivers broadcast and multicast to and from a Layer 3 adapter. It
// returns the process exit code: 0 on completion, 1 on a setup failure and 2
// when the prerequisites (administrator, wintun.dll) are missing.
func RunWintunSelfTest(ctx context.Context, w io.Writer, opt SelfTestOptions) int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintln(w, "wintun-selftest needs administrator rights to create a network adapter.")
		fmt.Fprintln(w, "Open an elevated terminal (Run as administrator) and run it again.")
		return 2
	}
	exe, _ := os.Executable()
	if !wintunDLLPresent(exe) {
		fmt.Fprintln(w, "wintun.dll was not found next to the executable.")
		fmt.Fprintf(w, "Download it from %s (wintun-*.zip, use the dll in the folder for your CPU, usually amd64)\n", WintunURL)
		fmt.Fprintf(w, "and copy it to %s\n", filepath.Dir(exe))
		return 2
	}

	fmt.Fprintf(w, "Creating Wintun adapter %q (MTU %d)...\n", selfTestAdapter, selfTestMTU)
	dev, err := tun.CreateTUN(selfTestAdapter, selfTestMTU)
	if err != nil {
		fmt.Fprintf(w, "cannot create the adapter: %v\n", err)
		return 1
	}
	nt, ok := dev.(*tun.NativeTun)
	if !ok {
		dev.Close()
		fmt.Fprintln(w, "unexpected tun device type")
		return 1
	}
	tap := newAdapterTap(dev)
	defer tap.close() // also removes the adapter
	luid := winipcfg.LUID(nt.LUID())

	if err := luid.SetIPAddresses([]netip.Prefix{stPrefix}); err != nil {
		fmt.Fprintf(w, "cannot assign %v: %v\n", stPrefix, err)
		return 1
	}
	if _, err := luid.Route(stPrefix.Masked(), netip.IPv4Unspecified()); err != nil {
		// Normally Windows adds the on-link route with the address.
		if err := luid.AddRoute(stPrefix.Masked(), netip.IPv4Unspecified(), 0); err != nil {
			fmt.Fprintf(w, "cannot add the on-link route: %v\n", err)
			return 1
		}
	}
	if err := waitAddressUsable(ctx); err != nil {
		fmt.Fprintf(w, "address %v is not usable: %v\n", stLocal, err)
		return 1
	}
	drv, _ := nt.RunningVersion()
	autoMetric := interfaceMetric(luid)
	fmt.Fprintf(w, "Adapter ready: %v, automatic metric %d, Wintun driver %d.%d\n", stPrefix, autoMetric, drv>>16, drv&0xffff)

	if !opt.NoFirewall && exe != "" {
		if addFirewallRule(exe) {
			fmt.Fprintln(w, "Added a temporary inbound firewall rule for this executable (removed at the end).")
			defer removeFirewallRule()
		} else {
			fmt.Fprintln(w, "warning: could not add the firewall rule; inbound tests (d) and (e) may fail because of Windows Firewall.")
		}
	}

	token := NewToken()
	rows := &selfTestRows{}

	fmt.Fprintln(w, "Running send tests (a)-(c) with the automatic metric...")
	auto := runSendTests(ctx, tap, token+"A", 47800)
	fmt.Fprintln(w, "Running receive tests (d)-(e)...")
	recv := runReceiveTests(ctx, tap, token)

	fmt.Fprintln(w, "Setting the adapter metric to 1 (compatibility mode) and repeating (a)-(c)...")
	var compat map[string]string
	if err := setInterfaceMetric(luid, 1); err != nil {
		fmt.Fprintf(w, "cannot set the metric: %v\n", err)
	} else {
		time.Sleep(time.Second)
		compat = runSendTests(ctx, tap, token+"B", 47810)
	}
	compatMetric := interfaceMetric(luid)

	for _, k := range sendTestOrder {
		c := "not measured"
		if compat != nil {
			c = compat[k.id]
		}
		rows.add(k.title, auto[k.id], c)
	}
	for _, r := range recv {
		rows.add(r[0], r[1], "(not affected by the metric)")
	}

	winver := windows.RtlGetVersion()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Windows %d.%d.%d, Wintun driver %d.%d, %s\n", winver.MajorVersion, winver.MinorVersion, winver.BuildNumber, drv>>16, drv&0xffff, time.Now().Format("2006-01-02"))
	fmt.Fprintf(w, "\n| Test | Default metric (%d) | Metric 1 (compatibility mode, now %d) |\n| --- | --- | --- |\n", autoMetric, compatMetric)
	for _, r := range rows.rows {
		fmt.Fprintf(w, "| %s | %s | %s |\n", r[0], r[1], r[2])
	}
	if compat != nil && auto["b-any"] != compat["b-any"] {
		fmt.Fprintf(w, "\nDifference: global broadcast from a 0.0.0.0 socket (b) is %q with the default metric and %q with metric 1.\n", auto["b-any"], compat["b-any"])
	} else if compat != nil {
		fmt.Fprintln(w, "\nNo difference between default and metric 1 for the global broadcast test (b).")
	}
	fmt.Fprintln(w, "\nCleaning up the adapter...")
	return 0
}

func wintunDLLPresent(exe string) bool {
	for _, dir := range []string{filepath.Dir(exe), os.Getenv("SystemRoot") + `\System32`} {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, "wintun.dll")); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

type selfTestRows struct{ rows [][3]string }

func (r *selfTestRows) add(a, b, c string) { r.rows = append(r.rows, [3]string{a, b, c}) }

func waitAddressUsable(ctx context.Context) error {
	var err error
	for i := 0; i < 50 && ctx.Err() == nil; i++ {
		var c *net.UDPConn
		c, err = net.ListenUDP("udp4", &net.UDPAddr{IP: stLocal.AsSlice()})
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}

func interfaceMetric(luid winipcfg.LUID) uint32 {
	row, err := luid.IPInterface(winipcfg.AddressFamily(windows.AF_INET))
	if err != nil {
		return 0
	}
	return row.Metric
}

func setInterfaceMetric(luid winipcfg.LUID, metric uint32) error {
	row, err := luid.IPInterface(winipcfg.AddressFamily(windows.AF_INET))
	if err != nil {
		return err
	}
	row.UseAutomaticMetric = false
	row.Metric = metric
	return row.Set()
}

func addFirewallRule(exe string) bool {
	removeFirewallRule()
	err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+selfTestRuleName, "dir=in", "action=allow", "protocol=UDP", "profile=any", "program="+exe).Run()
	return err == nil
}

func removeFirewallRule() {
	exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+selfTestRuleName).Run()
}

// adapterTap reads the packets that Windows sends into the adapter and
// writes packets that Windows should receive from it.
type adapterTap struct {
	dev  tun.Device
	pkts chan UDPPacket
	done chan struct{}
}

func newAdapterTap(dev tun.Device) *adapterTap {
	t := &adapterTap{dev: dev, pkts: make(chan UDPPacket, 4096), done: make(chan struct{})}
	go t.readLoop()
	return t
}

func (t *adapterTap) readLoop() {
	defer close(t.done)
	bufs := [][]byte{make([]byte, 4096)}
	sizes := make([]int, 1)
	for {
		n, err := t.dev.Read(bufs, sizes, 0)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		p, err := ParseIPv4UDP(bufs[0][:sizes[0]])
		if err != nil {
			continue // IPv6, IGMP, ICMP and other background traffic
		}
		p.Payload = append([]byte(nil), p.Payload...)
		select {
		case t.pkts <- p:
		default:
		}
	}
}

func (t *adapterTap) close() {
	t.dev.Close()
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
	}
}

// inject writes a UDP packet into the adapter, as if it had arrived from the
// network.
func (t *adapterTap) inject(src, dst netip.Addr, sport, dport uint16, payload []byte) error {
	_, err := t.dev.Write([][]byte{BuildIPv4UDP(src, dst, sport, dport, payload)}, 0)
	return err
}

// waitFor returns the first packet read from the adapter whose payload is the
// probe name for token.
func (t *adapterTap) waitFor(token, name string, timeout time.Duration) (UDPPacket, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case p := <-t.pkts:
			if pr, err := DecodeProbe(p.Payload); err == nil && pr.Token == token && pr.Name == name {
				return p, true
			}
		case <-deadline.C:
			return UDPPacket{}, false
		}
	}
}

var sendTestOrder = []struct{ id, title string }{
	{"a-any", "(a) App socket (0.0.0.0) to 10.77.0.255 read from adapter"},
	{"a-bound", "(a) Socket bound to 10.77.0.2 to 10.77.0.255 read from adapter"},
	{"b-any", "(b) App socket (0.0.0.0) to 255.255.255.255 read from adapter"},
	{"b-bound", "(b) Socket bound to 10.77.0.2 to 255.255.255.255 read from adapter"},
	{"c-if", "(c) Multicast 239.255.77.77 with IP_MULTICAST_IF=10.77.0.2 read from adapter"},
	{"c-default", "(c) Multicast 239.255.77.77 on the default interface read from adapter"},
}

// runSendTests sends from application sockets and reports, per test, whether
// the packet appeared on the adapter.
func runSendTests(ctx context.Context, tap *adapterTap, token string, port uint16) map[string]string {
	ap := func(a netip.Addr) netip.AddrPort { return netip.AddrPortFrom(a, port) }
	cases := map[string]udpCase{
		"a-any":     {id: "a-any", dst: ap(stSubnet)},
		"a-bound":   {id: "a-bound", dst: ap(stSubnet), bindIP: stLocal},
		"b-any":     {id: "b-any", dst: ap(stGlobal)},
		"b-bound":   {id: "b-bound", dst: ap(stGlobal), bindIP: stLocal},
		"c-if":      {id: "c-if", dst: ap(stGroup), bindIP: stLocal, mcastIF: true},
		"c-default": {id: "c-default", dst: ap(stGroup)},
	}
	out := map[string]string{}
	for _, k := range sendTestOrder {
		if ctx.Err() != nil {
			out[k.id] = "interrupted"
			continue
		}
		c := cases[k.id]
		if err := sendProbeCase(c, token, stLocal.String()); err != nil {
			out[k.id] = "no (send failed: " + err.Error() + ")"
			continue
		}
		if p, ok := tap.waitFor(token, k.id, 1500*time.Millisecond); ok {
			out[k.id] = fmt.Sprintf("yes (source %v, dest %v)", p.Src, p.Dst)
		} else {
			out[k.id] = "no"
		}
	}
	return out
}

// runReceiveTests writes packets into the adapter and checks which sockets
// receive them.
func runReceiveTests(ctx context.Context, tap *adapterTap, token string) [][2]string {
	var rows [][2]string
	type target struct {
		label string
		bind  func(port int) (*net.UDPConn, error)
	}
	targets := []target{
		{"socket bound to 0.0.0.0:port", func(p int) (*net.UDPConn, error) {
			return listenUDP4(fmt.Sprintf("0.0.0.0:%d", p), setBroadcast)
		}},
		{"socket bound to 10.77.0.2:port", func(p int) (*net.UDPConn, error) {
			return listenUDP4(fmt.Sprintf("10.77.0.2:%d", p), setBroadcast)
		}},
		{"socket bound to 10.77.0.255:port", func(p int) (*net.UDPConn, error) {
			return listenUDP4(fmt.Sprintf("10.77.0.255:%d", p), setBroadcast)
		}},
	}
	port := 47820
	for _, dst := range []struct {
		label string
		addr  netip.Addr
	}{{"10.77.0.255", stSubnet}, {"255.255.255.255", stGlobal}} {
		for _, tg := range targets {
			port++
			title := fmt.Sprintf("(d) Broadcast packet written into adapter (src 10.77.0.3, dst %s) received by %s", dst.label, tg.label)
			conn, err := tg.bind(port)
			if err != nil {
				rows = append(rows, [2]string{title, "n/a (bind failed: " + err.Error() + ")"})
				continue
			}
			rows = append(rows, [2]string{title, receiveInjected(ctx, tap, conn, token, fmt.Sprintf("d-%d", port), dst.addr, uint16(port))})
			conn.Close()
		}
	}
	// (e) multicast, receiver joined on the adapter.
	port++
	title := "(e) Multicast packet written into adapter (src 10.77.0.3, dst 239.255.77.77) received by socket joined on 10.77.0.2"
	conn, err := listenUDP4(fmt.Sprintf("0.0.0.0:%d", port), setReuseAddr)
	if err != nil {
		rows = append(rows, [2]string{title, "n/a (bind failed: " + err.Error() + ")"})
		return rows
	}
	defer conn.Close()
	if err := joinOn(conn, stGroup, stLocal); err != nil {
		rows = append(rows, [2]string{title, "n/a (join failed: " + err.Error() + ")"})
		return rows
	}
	rows = append(rows, [2]string{title, receiveInjected(ctx, tap, conn, token, "e", stGroup, uint16(port))})
	return rows
}

// receiveInjected injects three packets and waits for one to reach conn.
func receiveInjected(ctx context.Context, tap *adapterTap, conn *net.UDPConn, token, name string, dst netip.Addr, port uint16) string {
	for i := 0; i < 3; i++ {
		payload := Probe{Token: token, Name: name, Claimed: stRemote.String(), Seq: i}.Encode()
		if err := tap.inject(stRemote, dst, 40000, port, payload); err != nil {
			return "no (write to adapter failed: " + err.Error() + ")"
		}
		time.Sleep(30 * time.Millisecond)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if strings.Contains(err.Error(), "timeout") || time.Now().After(deadline) {
				return "no"
			}
			continue
		}
		if p, err := DecodeProbe(buf[:n]); err == nil && p.Token == token && p.Name == name {
			return fmt.Sprintf("yes (source %v)", src.Addr())
		}
	}
	return "interrupted"
}
