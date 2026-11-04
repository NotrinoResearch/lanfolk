package discovery

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/identity"
	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"
	"github.com/anacrolix/torrent/bencode"
	"golang.org/x/time/rate"
)

// DefaultBootstrap lists the well-known Mainline DHT routers.
var DefaultBootstrap = []string{
	"router.bittorrent.com:6881",
	"router.utorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.libtorrent.org:25401",
}

// ErrNotFound is returned by Lookup when no record exists on the DHT.
var ErrNotFound = errors.New("discovery: DHT record not found")

// DHTOptions configures StartDHT.
type DHTOptions struct {
	// Addr is the UDP listen address. It should be a separate socket from the
	// game socket. Default ":0".
	Addr string
	// Bootstrap lists host:port routers in addition to DefaultBootstrap.
	Bootstrap []string
	// NoBootstrapDefaults skips DefaultBootstrap. Tests use only local nodes.
	NoBootstrapDefaults bool
	// Serve makes the node answer queries and store items for other nodes. By
	// default the node is a read-only DHT client (BEP 43), which is what a
	// game client behind NAT should be. Tests that build a private DHT need
	// serving nodes.
	Serve  bool
	Logger *slog.Logger
}

// DHT is a Mainline DHT client used to publish and look up encrypted
// candidate records.
type DHT struct {
	srv    *dht.Server
	conn   net.PacketConn
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{} // closed when the initial bootstrap has finished
	log    *slog.Logger
}

// StartDHT opens the DHT socket and starts bootstrapping in the background.
// Publish and Lookup wait for the bootstrap to finish.
func StartDHT(ctx context.Context, opts DHTOptions) (*DHT, error) {
	if opts.Addr == "" {
		opts.Addr = ":0"
	}
	lg := opts.Logger
	if lg == nil {
		lg = slog.New(slog.DiscardHandler)
	}
	var hosts []string
	if !opts.NoBootstrapDefaults {
		hosts = append(hosts, DefaultBootstrap...)
	}
	hosts = append(hosts, opts.Bootstrap...)
	for _, h := range hosts {
		if _, _, err := net.SplitHostPort(h); err != nil {
			return nil, fmt.Errorf("discovery: bad bootstrap address %q: %w", h, err)
		}
	}
	conn, err := net.ListenPacket("udp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("discovery: listen DHT socket: %w", err)
	}

	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.Passive = !opts.Serve
	cfg.NoSecurity = true
	// Same rate as the library default, but not shared with other instances
	// in the process.
	cfg.SendLimiter = rate.NewLimiter(250, 25)
	cfg.StartingNodes = func() ([]dht.Addr, error) { return dht.ResolveHostPorts(hosts) }
	cfg.Logger = anacrolixLogger(lg)
	srv, err := dht.NewServer(cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("discovery: start DHT: %w", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	d := &DHT{srv: srv, conn: conn, ctx: cctx, cancel: cancel, ready: make(chan struct{}), log: lg}
	go func() {
		defer close(d.ready)
		if len(hosts) == 0 {
			return
		}
		stats, err := srv.BootstrapContext(cctx)
		if err != nil && cctx.Err() == nil {
			lg.Debug("DHT bootstrap failed", "err", err)
			return
		}
		lg.Debug("DHT bootstrap finished", "tried", stats.NumAddrsTried, "responses", stats.NumResponses, "nodes", srv.NumNodes())
	}()
	return d, nil
}

func anacrolixLogger(lg *slog.Logger) log.Logger {
	l := log.NewLogger("dht")
	l.Handlers = []log.Handler{log.SlogHandlerAsHandler{SlogHandler: lg.Handler()}}
	if lg.Enabled(context.Background(), slog.LevelDebug) {
		return l.FilterLevel(log.Debug)
	}
	return l.FilterLevel(log.Info)
}

func (d *DHT) waitReady(ctx context.Context) error {
	select {
	case <-d.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-d.ctx.Done():
		return errors.New("discovery: DHT closed")
	}
}

// Publish stores rec as the record published by pair.Local. The BEP 44
// sequence number is the publication time in seconds, raised above any
// sequence number already on the DHT.
func (d *DHT) Publish(ctx context.Context, pair *identity.Pair, rec Record) error {
	value, err := SealRecord(pair, rec)
	if err != nil {
		return err
	}
	if err := d.waitReady(ctx); err != nil {
		return err
	}
	salt := pair.DHTSalt(pair.Local)
	priv := pair.DHTSigningKey(pair.Local)
	var pub [32]byte
	copy(pub[:], priv.Public().(ed25519.PublicKey))
	target := bep44.MakeMutableTarget(pub, salt)
	want := rec.Published.Unix()
	stats, err := getput.Put(ctx, krpc.ID(target), d.srv, salt, func(cur int64) bep44.Put {
		seq := want
		if seq <= cur {
			seq = cur + 1
		}
		p := bep44.Put{V: value, K: &pub, Salt: salt, Seq: seq}
		p.Sign(priv)
		return p
	})
	if err != nil {
		return fmt.Errorf("discovery: DHT publish: %w", err)
	}
	if stats == nil || stats.NumResponses == 0 {
		return errors.New("discovery: DHT publish: no DHT node responded")
	}
	d.log.Debug("DHT record published", "responses", stats.NumResponses)
	return nil
}

// Lookup fetches and decrypts the newest record published by pair.Remote.
// Records older than maxAge are rejected with ErrRecordStale.
func (d *DHT) Lookup(ctx context.Context, pair *identity.Pair, maxAge time.Duration) (Record, error) {
	if err := d.waitReady(ctx); err != nil {
		return Record{}, err
	}
	salt := pair.DHTSalt(pair.Remote)
	pubKey := pair.DHTSigningKey(pair.Remote).Public().(ed25519.PublicKey)
	var pub [32]byte
	copy(pub[:], pubKey)
	target := bep44.MakeMutableTarget(pub, salt)
	res, _, err := getput.Get(ctx, target, d.srv, nil, salt)
	if err != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		return Record{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	if !res.Mutable {
		return Record{}, ErrNotFound
	}
	var value []byte
	if err := bencode.Unmarshal(res.V, &value); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrRecordInvalid, err)
	}
	return OpenRecord(pair, value, maxAge, time.Now())
}

// Nodes returns the number of nodes in the routing table.
func (d *DHT) Nodes() int { return d.srv.NumNodes() }

// Addr returns the local UDP address of the DHT socket.
func (d *DHT) Addr() netip.AddrPort {
	if ua, ok := d.srv.Addr().(*net.UDPAddr); ok {
		ap := ua.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}

// Close stops the DHT node and closes its socket.
func (d *DHT) Close() {
	d.cancel()
	d.srv.Close()
	d.conn.Close()
}
