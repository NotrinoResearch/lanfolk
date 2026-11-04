// Command lanprobe tests LAN behaviors between two machines over the Lanfolk
// virtual network without needing a game: unicast, broadcast, multicast,
// IP-in-payload, private range checks, blocked ports and tunnel latency.
//
// Usage:
//
//	lanprobe serve [-bind 0.0.0.0] [-iface-ip 10.77.0.3]
//	lanprobe test -peer 10.77.0.3 [-local 10.77.0.2]
//	lanprobe latency -peer 10.77.0.3 [-compare 192.168.1.20] [-count 10000] [-size 200] [-rate 64]
//	lanprobe wintun-selftest            (Windows, administrator)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/lanprobe"
)

const usage = `lanprobe tests LAN behaviors over the Lanfolk virtual network.

Commands:
  serve            run the probe server (TCP 7790, UDP 7791, 7792, 7793, 7794)
  test             run all behavior tests against a peer that runs "serve"
  latency          measure UDP round trip times against a peer that runs "serve"
  wintun-selftest  (Windows, administrator) test broadcast and multicast through Wintun

Run "lanprobe <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var code int
	switch os.Args[1] {
	case "serve":
		code = cmdServe(ctx, os.Args[2:])
	case "test":
		code = cmdTest(ctx, os.Args[2:])
	case "latency":
		code = cmdLatency(ctx, os.Args[2:])
	case "wintun-selftest":
		code = cmdWintun(ctx, os.Args[2:])
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		code = 2
	}
	stop()
	os.Exit(code)
}

func cmdServe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	bind := fs.String("bind", "0.0.0.0", "address for the TCP, UDP unicast and latency sockets")
	iface := fs.String("iface-ip", "", "join the multicast group only on the interface with this IPv4 address (default: all interfaces)")
	fs.Parse(args)
	srv, err := lanprobe.NewServer(lanprobe.ServerConfig{
		Bind:    *bind,
		IfaceIP: *iface,
		Logf:    func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot start the server:", err)
		return 1
	}
	p := srv.Ports()
	fmt.Printf("lanprobe server ready: TCP %d (control and TCP unicast), UDP %d (unicast echo), UDP %d (broadcast), UDP %d (multicast %s), UDP %d (latency echo)\n",
		p.TCP, p.Unicast, p.Broadcast, p.Multicast, lanprobe.DefaultGroup, p.Latency)
	if j := srv.JoinedInterfaces(); len(j) > 0 {
		fmt.Printf("joined %s on: %s\n", lanprobe.DefaultGroup, strings.Join(j, ", "))
	}
	fmt.Println("allow these ports in Windows Firewall for the Lanfolk adapter; press Ctrl+C to stop")
	srv.Serve(ctx)
	return 0
}

func cmdTest(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	peer := fs.String("peer", "", "IPv4 address of the peer that runs `lanprobe serve` (required)")
	local := fs.String("local", "", "this machine's virtual IPv4 address (default: the address used to reach the peer)")
	subnet := fs.String("subnet-broadcast", "10.77.0.255", "directed broadcast address of the virtual network")
	skip := fs.String("skip", "", "comma-separated test IDs to skip: "+strings.Join(testIDs, ","))
	jsonOut := fs.String("json", "", "also write the results as JSON to this file")
	fs.Parse(args)
	if *peer == "" {
		fmt.Fprintln(os.Stderr, "-peer is required")
		return 2
	}
	skips := map[string]bool{}
	for _, s := range strings.Split(*skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			skips[s] = true
		}
	}
	cfg := lanprobe.TestConfig{
		Peer: *peer, Local: *local, SubnetBroadcast: *subnet, Skip: skips,
		Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	results, err := lanprobe.RunTest(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test failed:", err)
		return 2
	}
	fmt.Println()
	lanprobe.WriteResults(os.Stdout, results)
	label := "lanprobe " + *peer
	hdr, row := lanprobe.MarkdownRow(label, results)
	fmt.Printf("\nMarkdown for docs/MEASUREMENTS.md (edit the first cell):\n%s\n%s\n", hdr, row)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "cannot write JSON:", err)
		}
	}
	if lanprobe.Failed(results) {
		fmt.Println("\nRESULT: FAIL (see the FAIL lines above; lines marked informational do not count)")
		return 1
	}
	fmt.Println("\nRESULT: PASS (lines marked informational do not count)")
	return 0
}

var testIDs = []string{
	lanprobe.IDTCP, lanprobe.IDUDP, lanprobe.IDSubnetBcast, lanprobe.IDGlobalBound, lanprobe.IDGlobalAny,
	lanprobe.IDMcastIF, lanprobe.IDMcastDefault, lanprobe.IDIPInPayload, lanprobe.IDPrivateRange, lanprobe.IDBlockedPorts,
}

func cmdLatency(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("latency", flag.ExitOnError)
	peer := fs.String("peer", "", "IPv4 address of the peer that runs `lanprobe serve` (virtual IP when -compare is used)")
	compare := fs.String("compare", "", "raw (real LAN or internet) IPv4 address of the same peer; measures both and prints the tunnel overhead")
	count := fs.Int("count", 10000, "packets to send")
	size := fs.Int("size", 200, "datagram size in bytes")
	rate := fs.Float64("rate", 64, "packets per second")
	fs.Parse(args)
	if *peer == "" {
		fmt.Fprintln(os.Stderr, "-peer is required")
		return 2
	}
	mk := func(ip string) lanprobe.LatencyConfig {
		return lanprobe.LatencyConfig{Peer: ip, Count: *count, Size: *size, Rate: *rate}
	}
	eta := time.Duration(float64(*count) / *rate * float64(time.Second))
	if *compare == "" {
		fmt.Printf("sending %d packets of %d bytes at %.0f/s to %s (about %s)\n", *count, *size, *rate, *peer, eta.Round(time.Second))
		cfg := mk(*peer)
		cfg.Progress = func(sent int) { fmt.Fprintf(os.Stderr, "\r%d/%d sent", sent, *count) }
		s, err := lanprobe.RunLatency(ctx, cfg)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "latency:", err)
			return 1
		}
		lanprobe.WriteSummary(os.Stdout, *peer, s)
		return 0
	}
	fmt.Printf("sending %d packets of %d bytes at %.0f/s to each of %s (tunnel) and %s (raw) at the same time (about %s)\n",
		*count, *size, *rate, *peer, *compare, eta.Round(time.Second))
	raw, tun, err := lanprobe.RunLatencyCompare(ctx, mk(*compare), mk(*peer))
	if err != nil {
		fmt.Fprintln(os.Stderr, "latency:", err)
		return 1
	}
	lanprobe.WriteSummary(os.Stdout, "raw", raw)
	lanprobe.WriteSummary(os.Stdout, "tunnel", tun)
	if raw.Received == 0 || tun.Received == 0 {
		fmt.Println("no replies on one of the paths, cannot compute the overhead")
		return 1
	}
	o := lanprobe.ComputeOverhead(raw, tun)
	lanprobe.WriteOverhead(os.Stdout, o)
	if !o.OKP50 || !o.OKP99 {
		return 1
	}
	return 0
}

func cmdWintun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("wintun-selftest", flag.ExitOnError)
	noFW := fs.Bool("no-firewall", false, "do not add a temporary inbound firewall rule for this executable")
	fs.Parse(args)
	return lanprobe.RunWintunSelfTest(ctx, os.Stdout, lanprobe.SelfTestOptions{NoFirewall: *noFW})
}
