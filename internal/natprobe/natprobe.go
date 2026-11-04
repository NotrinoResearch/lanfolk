package natprobe

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// DefaultSTUN lists the public STUN servers used when none are given.
var DefaultSTUN = []string{"stun.l.google.com:19302", "stun1.l.google.com:19302", "stun.cloudflare.com:3478"}

// DefaultTimeouts are the idle times (seconds) tested for the NAT mapping.
var DefaultTimeouts = []int{15, 30, 60, 90, 120}

// Config configures a run.
type Config struct {
	Relay        string   // relay host; empty skips the relay tests
	RelayUDPPort int      // default 3478
	RelayTCPPort int      // default 443
	STUN         []string // public STUN servers, default DefaultSTUN
	Timeouts     []int    // idle times in seconds, default DefaultTimeouts
	SkipDHT      bool
	ISP          string
	WANIP        string // optional router WAN IP for the CGNAT check
	// DHTWait is how long to wait for the DHT bootstrap. Default 30 s.
	DHTWait time.Duration
	// IPv6STUN is the STUN server for the IPv6 check.
	IPv6STUN string
	SkipIPv6 bool
	// DHTBootstrap overrides the default bootstrap routers (host:port).
	DHTBootstrap []string
	Logf         func(format string, args ...any)
}

func (c *Config) defaults() {
	if c.RelayUDPPort == 0 {
		c.RelayUDPPort = 3478
	}
	if c.RelayTCPPort == 0 {
		c.RelayTCPPort = 443
	}
	if len(c.STUN) == 0 {
		c.STUN = DefaultSTUN
	}
	if c.Timeouts == nil {
		c.Timeouts = DefaultTimeouts
	}
	if c.DHTWait <= 0 {
		c.DHTWait = 30 * time.Second
	}
	if c.IPv6STUN == "" {
		c.IPv6STUN = "stun.l.google.com:19302"
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// MappingReport is the mapping behavior result.
type MappingReport struct {
	LocalPort    uint16       `json:"local_port"`
	Observations []Obs        `json:"observations"`
	Class        MappingClass `json:"class"`
	Error        string       `json:"error,omitempty"`
}

// SecondSocketReport is the result of the second socket check.
type SecondSocketReport struct {
	LocalPort    uint16     `json:"local_port"`
	Observations []Obs      `json:"observations"`
	Allocation   AllocClass `json:"allocation"`
	Error        string     `json:"error,omitempty"`
}

// TimeoutReport is the NAT mapping timeout result.
type TimeoutReport struct {
	Skipped   string          `json:"skipped,omitempty"`
	Results   []TimeoutResult `json:"results,omitempty"`
	Keepalive KeepaliveRec    `json:"keepalive"`
	TookS     float64         `json:"took_s,omitempty"`
}

// Report is everything natprobe measured.
type Report struct {
	Tool     string              `json:"tool"`
	Time     time.Time           `json:"time"`
	ISP      string              `json:"isp"`
	PublicIP string              `json:"public_ip,omitempty"`
	Mapping  MappingReport       `json:"mapping"`
	Second   *SecondSocketReport `json:"second_socket,omitempty"`
	Relay    RelayReport         `json:"relay"`
	Timeout  TimeoutReport       `json:"nat_timeout"`
	IPv6     IPv6Report          `json:"ipv6"`
	DHT      DHTReport           `json:"dht"`
	CGNAT    CGNATReport         `json:"cgnat"`
	Notes    []string            `json:"notes,omitempty"`
}

// Run performs every measurement. Failures are recorded in the report; Run
// itself never fails.
func Run(ctx context.Context, cfg Config) *Report {
	cfg.defaults()
	logf := cfg.Logf
	r := &Report{Tool: "natprobe", Time: time.Now().UTC().Truncate(time.Second), ISP: cfg.ISP}
	sc := &scrubber{}
	local := LocalAddrs()

	// Resolve the relay and the STUN servers.
	var relayUDP, relayTCP netip.AddrPort
	var targets []Target
	if cfg.Relay != "" {
		sc.add(cfg.Relay)
		ip, err := resolveV4(ctx, cfg.Relay)
		if err != nil {
			r.Notes = append(r.Notes, "relay: "+sc.err(err))
		} else {
			sc.add(ip.String())
			relayUDP = netip.AddrPortFrom(ip, uint16(cfg.RelayUDPPort))
			relayTCP = netip.AddrPortFrom(ip, uint16(cfg.RelayTCPPort))
			r.Relay.Configured = true
			targets = append(targets, Target{Label: "relay", Addr: relayUDP})
		}
	} else {
		r.Notes = append(r.Notes, "no -relay given: relay RTT, TCP fallback and NAT timeout tests were skipped")
	}
	stunTargets, problems := ResolveSTUN(ctx, cfg.STUN)
	targets = append(targets, stunTargets...)
	for _, p := range problems {
		r.Notes = append(r.Notes, "stun: "+p)
	}
	if len(stunTargets) < 2 {
		r.Notes = append(r.Notes, fmt.Sprintf("only %d public STUN server(s) resolved; mapping classification is less reliable", len(stunTargets)))
	}

	// 1. Mapping behavior from one socket.
	logf("mapping behavior: STUN queries from one socket to %d destinations...", len(targets))
	var firstPorts []uint16
	var lp1 uint16
	conn1, err := net.ListenUDP("udp4", nil)
	if err != nil {
		r.Mapping.Error = err.Error()
	} else {
		lp1 = localPort(conn1)
		r.Mapping.LocalPort = lp1
		r.Mapping.Observations = QueryTargets(ctx, conn1, targets, sc)
		r.Mapping.Class = ClassifyMapping(lp1, r.Mapping.Observations)
		firstPorts = mappedPorts(r.Mapping.Observations)
		conn1.Close()
	}
	if r.Mapping.Class.Behavior == "" {
		r.Mapping.Class = ClassifyMapping(0, nil)
	}
	var mapped netip.Addr
	for _, o := range r.Mapping.Observations {
		if o.Mapped.IsValid() {
			mapped = o.Mapped.Addr()
			r.PublicIP = mapped.String()
			break
		}
	}

	// 2. A second socket shows the port allocation pattern.
	logf("second socket check...")
	if ctx.Err() == nil && r.Mapping.Class.Answers > 0 {
		sec := &SecondSocketReport{}
		conn2, err := net.ListenUDP("udp4", nil)
		if err != nil {
			sec.Error = err.Error()
		} else {
			sec.LocalPort = localPort(conn2)
			sec.Observations = QueryTargets(ctx, conn2, targets, sc)
			sec.Allocation = ClassifyAllocation(lp1, firstPorts, sec.LocalPort, mappedPorts(sec.Observations))
			conn2.Close()
		}
		r.Second = sec
	}

	// 5. IPv6 (quick).
	if cfg.SkipIPv6 {
		r.IPv6.Err = "skipped"
	} else {
		logf("IPv6...")
		r.IPv6 = MeasureIPv6(ctx, local, cfg.IPv6STUN)
	}

	// 3. Relay reachability.
	if r.Relay.Configured {
		logf("relay UDP and TCP round trips (%d samples each)...", pingSamples)
		if s, err := PingUDP(ctx, relayUDP, sc); err != nil {
			r.Relay.UDPErr = err.Error()
		} else {
			r.Relay.UDP = s
		}
		s, conMs, err := PingTCP(ctx, relayTCP, sc)
		r.Relay.TCP, r.Relay.TCPConnect = s, conMs
		if err != nil {
			r.Relay.TCPErr = err.Error()
			if s != nil && s.Received == 0 {
				r.Relay.TCP = nil
			}
		}
	}

	// 4 and 6 run in parallel: the timeout test is mostly waiting.
	var wg sync.WaitGroup
	switch {
	case !r.Relay.Configured:
		r.Timeout.Skipped = "no relay"
	case len(cfg.Timeouts) == 0:
		r.Timeout.Skipped = "no timeouts requested"
	case r.Relay.UDP == nil:
		r.Timeout.Skipped = "relay UDP is not reachable"
	default:
		wg.Add(1)
		go func() {
			defer wg.Done()
			maxT := 0
			for _, t := range cfg.Timeouts {
				maxT = max(maxT, t)
			}
			logf("NAT mapping timeout: waiting up to %d s (parallel)...", maxT+4)
			start := time.Now()
			r.Timeout.Results = MeasureTimeouts(ctx, relayUDP, cfg.Timeouts, sc)
			r.Timeout.Keepalive = RecommendKeepalive(r.Timeout.Results)
			r.Timeout.TookS = time.Since(start).Seconds()
		}()
	}
	if cfg.SkipDHT {
		r.DHT.Skipped = true
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logf("DHT: bootstrap (up to %s), then BEP 44 put/get...", cfg.DHTWait)
			var pub net.IP
			if mapped.IsValid() && mapped.Is4() {
				pub = net.IP(mapped.AsSlice())
			}
			r.DHT = MeasureDHT(ctx, pub, cfg.DHTWait, cfg.DHTBootstrap)
		}()
	}
	wg.Wait()

	// 7. CGNAT hint.
	var wan netip.Addr
	if cfg.WANIP != "" {
		if ip, err := netip.ParseAddr(cfg.WANIP); err == nil {
			wan = ip
		} else {
			r.Notes = append(r.Notes, "ignoring invalid -wan-ip")
		}
	}
	r.CGNAT = CGNATHint(local, mapped, wan)
	for i := range r.Notes {
		r.Notes[i] = sc.text(r.Notes[i])
	}
	return r
}
