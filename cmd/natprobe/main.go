// Command natprobe measures the NAT behavior of an internet line so that
// keepalive intervals can be chosen and hole punching expectations set:
// UDP mapping behavior, port allocation, relay reachability (UDP and TCP),
// NAT mapping timeout, IPv6 and DHT access.
//
// Usage:
//
//	natprobe -relay relay.example.org -isp "Superonline" -json out.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/NotrinoResearch/lanfolk/internal/natprobe"
)

func main() {
	relay := flag.String("relay", "", "relay host (needed for the relay RTT, TCP fallback and NAT timeout tests; never printed)")
	udpPort := flag.Int("relay-udp-port", 3478, "relay UDP port")
	tcpPort := flag.Int("relay-tcp-port", 443, "relay TCP fallback port")
	stunList := flag.String("stun", strings.Join(natprobe.DefaultSTUN, ","), "comma-separated public STUN servers (host:port); at least two with different IPs")
	timeouts := flag.String("timeouts", "15,30,60,90,120", "comma-separated NAT idle times to test, in seconds (0-255; the relay caps the delay at 180)")
	skipDHT := flag.Bool("skip-dht", false, "skip the DHT access test")
	jsonOut := flag.String("json", "", "also write the full report as JSON to this file (contains your public IP)")
	isp := flag.String("isp", "", "label of the line being measured, for the Markdown row")
	wan := flag.String("wan-ip", "", "optional: the WAN IP shown by your router, to tell single NAT from CGNAT")
	flag.Parse()

	ts, err := parseTimeouts(*timeouts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "-timeouts:", err)
		os.Exit(2)
	}
	var servers []string
	for _, s := range strings.Split(*stunList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			servers = append(servers, s)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rep := natprobe.Run(ctx, natprobe.Config{
		Relay: *relay, RelayUDPPort: *udpPort, RelayTCPPort: *tcpPort,
		STUN: servers, Timeouts: ts, SkipDHT: *skipDHT, ISP: *isp, WANIP: *wan,
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
	})

	fmt.Println()
	rep.WriteSummary(os.Stdout)
	fmt.Printf("\nMarkdown row for docs/MEASUREMENTS.md (no IPs, no relay host):\n%s\n%s\n", natprobe.MarkdownHeader, rep.MarkdownRow())
	if *jsonOut != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err == nil {
			err = os.WriteFile(*jsonOut, b, 0o600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot write JSON:", err)
			os.Exit(1)
		}
		fmt.Printf("\nfull report written to %s (it contains your public IP: do not share it unredacted)\n", *jsonOut)
	}
}

func parseTimeouts(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return []int{}, nil
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 || v > 255 {
			return nil, fmt.Errorf("invalid value %q (want 0-255)", p)
		}
		out = append(out, v)
	}
	return out, nil
}
