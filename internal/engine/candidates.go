package engine

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/pathmgr"
	"github.com/NotrinoResearch/lanfolk/internal/portmap"
)

type candidate struct {
	addr   netip.AddrPort
	source pathmgr.Source
}

// collectCandidates gathers our addresses: interface IPs (local and public
// IPv6), the STUN-observed address from each relay, the port mapping and
// the manual port.
func (e *Engine) collectCandidates() []candidate {
	port := e.sock.Port()
	var out []candidate
	seen := map[netip.AddrPort]bool{}
	add := func(a netip.AddrPort, s pathmgr.Source) {
		a = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
		if !a.IsValid() || seen[a] {
			return
		}
		seen[a] = true
		out = append(out, candidate{addr: a, source: s})
	}
	if e.cfg.LoopbackCandidates {
		add(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port), pathmgr.SourceLocal)
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			switch {
			case network.Prefix.Contains(ip), ip.IsLinkLocalUnicast(), ip.IsLoopback(), ip.IsUnspecified():
				continue
			case ip.Is4():
				add(netip.AddrPortFrom(ip, port), pathmgr.SourceLocal)
			case ip.Is6() && ip.IsGlobalUnicast() && !ip.IsPrivate():
				add(netip.AddrPortFrom(ip, port), pathmgr.SourceIPv6)
			}
		}
	}
	if rs := e.relayIx.Load(); rs != nil {
		for _, r := range *rs {
			if o := r.client.Observed(); o.IsValid() {
				add(o, pathmgr.SourceSTUN)
				if e.settings.ManualPort > 0 {
					add(netip.AddrPortFrom(o.Addr(), uint16(e.settings.ManualPort)), pathmgr.SourceManual)
				}
			}
		}
	}
	if e.portmap != nil {
		if m, ok := e.portmap.m.Current(); ok {
			add(m.External, pathmgr.SourcePortMap)
		}
	}
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

func sameCandidates(a, b []candidate) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(c []candidate) []string {
		s := make([]string, len(c))
		for i, x := range c {
			s[i] = x.addr.String()
		}
		sort.Strings(s)
		return s
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// refreshCandidates recollects candidates. When they change, peers get the
// new list and probing restarts (network change, plan rule 8).
func (e *Engine) refreshCandidates(force bool) {
	e.mu.Lock()
	if !force && time.Since(e.candAt) < 10*time.Second {
		e.mu.Unlock()
		return
	}
	e.candAt = time.Now()
	e.mu.Unlock()
	c := e.collectCandidates()
	e.mu.Lock()
	if sameCandidates(c, e.cands) {
		e.mu.Unlock()
		return
	}
	first := e.cands == nil
	e.cands = c
	e.candGen++
	var managed []*peer
	for _, p := range e.peers {
		if p.managed {
			managed = append(managed, p)
		}
	}
	e.mu.Unlock()
	if first {
		return
	}
	e.log.Info("local addresses changed; restarting probes", "candidates", len(c))
	now := nanotime()
	for _, p := range managed {
		p.mu.Lock()
		if p.pm != nil {
			p.pm.Restart(now)
		}
		p.mu.Unlock()
		e.maybeSendCandidates(p, time.Now())
	}
}

// portMapper keeps an optional UPnP / NAT-PMP / PCP mapping of our port.
type portMapper struct {
	m      *portmap.Mapper
	cancel context.CancelFunc
}

func startPortMapper(ctx context.Context, e *Engine) *portMapper {
	if !e.settings.PortMapping {
		return nil
	}
	m := portmap.New(e.sock.Port(), portmap.Options{Logger: e.log.With("component", "portmap")})
	m.OnChange(func(portmap.Mapping, bool) { go e.refreshCandidates(true); go e.publishStatus() })
	cctx, cancel := context.WithCancel(ctx)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); m.Run(cctx) }()
	return &portMapper{m: m, cancel: cancel}
}
