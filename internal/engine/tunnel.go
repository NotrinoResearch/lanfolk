package engine

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/NotrinoResearch/lanfolk/internal/network"
	"github.com/NotrinoResearch/lanfolk/internal/tunwrap"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// AdapterName is the virtual adapter name.
const AdapterName = "Lanfolk"

// tunnel is the data plane of the active network.
type tunnel struct {
	dev        tun.Device
	wrap       *tunwrap.Device
	wg         *device.Device
	adapter    Adapter
	self       netip.Addr
	peers      map[identity.PublicKey]netip.Addr
	stopCap    func()
	cancel     context.CancelFunc
	compatMode bool
}

// startTunnelLocked creates the adapter and WireGuard device for the active
// network. Failures are reported in the status; the control plane keeps
// running so friends and membership still work.
func (e *Engine) startTunnelLocked() {
	if e.tunnel != nil || e.active == nil || e.cfg.NewTUN == nil {
		return
	}
	self, err := e.selfAddressLocked()
	if err != nil {
		e.tunErr = err.Error()
		return
	}
	t, err := e.buildTunnel(self.VirtualIP)
	if err != nil {
		e.tunErr = err.Error()
		e.log.Error("virtual adapter could not be started", "err", err)
		go e.publishStatus()
		return
	}
	e.tunErr = ""
	e.tunnel = t
	e.syncTunnelPeersLocked()
	e.log.Info("virtual adapter up", "address", self.VirtualIP.String())
	go e.publishStatus()
}

func (e *Engine) buildTunnel(self netip.Addr) (*tunnel, error) {
	dev, err := e.cfg.NewTUN(AdapterName, MTU)
	if err != nil {
		return nil, fmt.Errorf("create adapter: %w", err)
	}
	t := &tunnel{dev: dev, self: self, peers: make(map[identity.PublicKey]netip.Addr)}
	selfAddr := self
	var servicePort func(uint8, uint16) bool
	if e.cfg.ServicePort != nil {
		servicePort = e.servicePort
	}
	t.wrap = tunwrap.Wrap(dev, tunwrap.Config{
		Self:        func() netip.Addr { return selfAddr },
		Peers:       e.onlinePeerVIPs,
		Blocked:     e.cfg.Blocked,
		ServicePort: servicePort,
		Prefix:      network.Prefix,
		Logger:      e.log.With("component", "lan"),
	})
	if e.cfg.NewAdapter != nil {
		a, err := e.cfg.NewAdapter(dev)
		if err != nil {
			dev.Close()
			return nil, fmt.Errorf("configure adapter: %w", err)
		}
		if err := a.Configure(netip.PrefixFrom(self, network.Prefix.Bits()), MTU); err != nil {
			a.Close()
			dev.Close()
			return nil, fmt.Errorf("configure adapter: %w", err)
		}
		if err := a.SetCompatMode(e.settings.CompatMode); err != nil {
			e.log.Warn("compatibility mode could not be applied", "err", err)
		}
		t.compatMode = e.settings.CompatMode
		go func() {
			if err := a.SetPrivate(); err != nil {
				e.log.Warn("adapter could not be marked Private", "err", err)
			}
		}()
		t.adapter = a
	}
	logger := &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(f string, args ...any) { e.log.Debug("wireguard: " + fmt.Sprintf(f, args...)) },
	}
	t.wg = device.NewDevice(t.wrap, e.bind, logger)
	// Every peer's endpoint names the peer, and the path manager picks the
	// address at send time. WireGuard must not roam an endpoint to the source
	// of a packet: that would bypass path selection and Relay only.
	t.wg.DisableSomeRoamingForBrokenMobileSemantics()
	priv := e.key.X25519Private()
	if err := t.wg.IpcSet("private_key=" + hex.EncodeToString(priv[:]) + "\n"); err != nil {
		t.wg.Close()
		return nil, fmt.Errorf("configure WireGuard: %w", err)
	}
	if err := t.wg.Up(); err != nil {
		t.wg.Close()
		return nil, fmt.Errorf("start WireGuard: %w", err)
	}
	if e.cfg.StartCapture != nil && !e.settings.CompatMode {
		ctx, cancel := context.WithCancel(e.ctx)
		t.cancel = cancel
		stop, err := e.cfg.StartCapture(ctx, t.wrap.InjectLocalBroadcast)
		if err != nil {
			e.log.Warn("global broadcast capture is unavailable; compatibility mode may help", "err", err)
		} else {
			t.stopCap = stop
		}
	}
	return t, nil
}

func (e *Engine) stopTunnelLocked() {
	t := e.tunnel
	if t == nil {
		return
	}
	e.tunnel = nil
	if t.stopCap != nil {
		t.stopCap()
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.adapter != nil {
		t.adapter.Close()
	}
	t.wg.Close() // closes the TUN device and the bind
	e.log.Info("virtual adapter down")
}

// updateTunnelAddressLocked restarts the tunnel if our own address changed
// (for example the owner re-created our membership).
func (e *Engine) updateTunnelAddressLocked() {
	if e.tunnel == nil {
		return
	}
	self, err := e.selfAddressLocked()
	if err != nil || self.VirtualIP == e.tunnel.self {
		return
	}
	e.stopTunnelLocked()
	e.startTunnelLocked()
}

// syncTunnelPeersLocked makes the WireGuard peers match the active network
// members. Removed members lose their tunnel immediately.
func (e *Engine) syncTunnelPeersLocked() {
	t := e.tunnel
	if t == nil {
		return
	}
	want := make(map[identity.PublicKey]netip.Addr)
	for _, p := range e.peers {
		if p.managed && p.vip.IsValid() {
			want[p.key] = p.vip
		}
	}
	var b strings.Builder
	for pk := range t.peers {
		if _, ok := want[pk]; !ok {
			x, err := pk.X25519()
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "public_key=%s\nremove=true\n", hex.EncodeToString(x[:]))
		}
	}
	for pk, ip := range want {
		if old, ok := t.peers[pk]; ok && old == ip {
			continue
		}
		x, err := pk.X25519()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "public_key=%s\nreplace_allowed_ips=true\nallowed_ip=%s/32\nendpoint=%s\n",
			hex.EncodeToString(x[:]), ip, e.peers[pk].ep.DstToString())
	}
	if b.Len() == 0 {
		return
	}
	if err := t.wg.IpcSet(b.String()); err != nil {
		e.log.Error("updating WireGuard peers failed", "err", err)
		return
	}
	t.peers = want
}

// onlinePeerVIPs lists virtual IPs of members with a usable path; LAN
// broadcasts are copied to them.
func (e *Engine) onlinePeerVIPs() []netip.Addr {
	m := e.byKeyID.Load()
	if m == nil {
		return nil
	}
	var out []netip.Addr
	for _, p := range *m {
		if v := p.vipp.Load(); v != nil && p.active.Load() != nil {
			out = append(out, *v)
		}
	}
	return out
}

// setCompatModeLocked switches compatibility mode on the running adapter.
func (e *Engine) setCompatModeLocked(on bool) {
	t := e.tunnel
	if t == nil || t.compatMode == on {
		return
	}
	if t.adapter != nil {
		if err := t.adapter.SetCompatMode(on); err != nil {
			e.log.Warn("compatibility mode change failed", "err", err)
			return
		}
	}
	t.compatMode = on
	// Capture mode and compatibility mode are alternatives; restart the
	// tunnel so the capture follows the setting.
	e.stopTunnelLocked()
	e.startTunnelLocked()
}
