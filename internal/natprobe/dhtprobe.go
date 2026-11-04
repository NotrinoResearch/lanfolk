package natprobe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	alog "github.com/anacrolix/log"

	"github.com/NotrinoResearch/lanfolk/internal/clock"
)

// DHTReport is the DHT access result.
type DHTReport struct {
	Skipped     bool    `json:"skipped,omitempty"`
	Nodes       int     `json:"nodes"`
	GoodNodes   int     `json:"good_nodes"`
	FirstNodeMs float64 `json:"first_node_ms,omitempty"`
	BootstrapMs float64 `json:"bootstrap_ms,omitempty"`
	PutOK       bool    `json:"put_ok"`
	PutMs       float64 `json:"put_ms,omitempty"`
	PutAnswers  uint32  `json:"put_traversal_answers,omitempty"`
	GetOK       bool    `json:"get_ok"`
	GetMs       float64 `json:"get_ms,omitempty"`
	Err         string  `json:"error,omitempty"`
}

// MeasureDHT starts a DHT node on its own socket, bootstraps it with the
// default routers (up to bootstrapWait), then stores and fetches a random
// throwaway BEP 44 mutable item. publicIP, if valid, is used to derive a
// BEP 42 secure node ID.
func MeasureDHT(ctx context.Context, publicIP net.IP, bootstrapWait time.Duration, bootstrap []string) DHTReport {
	var r DHTReport
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		r.Err = "cannot open the DHT socket: " + err.Error()
		return r
	}
	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	cfg.Logger = alog.Default.WithFilterLevel(alog.Disabled)
	if len(bootstrap) > 0 {
		cfg.StartingNodes = func() ([]dht.Addr, error) { return dht.ResolveHostPorts(bootstrap) }
	}
	if publicIP != nil {
		cfg.PublicIP = publicIP
		cfg.NoSecurity = false
	}
	s, err := dht.NewServer(cfg)
	if err != nil {
		conn.Close()
		r.Err = "cannot start the DHT node: " + err.Error()
		return r
	}
	defer s.Close()

	bctx, cancel := context.WithTimeout(ctx, bootstrapWait)
	defer cancel()
	start := clock.Nanotime()
	boot := make(chan error, 1)
	go func() {
		_, err := s.BootstrapContext(bctx)
		boot <- err
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	var bootErr error
wait:
	for {
		select {
		case bootErr = <-boot:
			break wait
		case <-tick.C:
			if r.FirstNodeMs == 0 && s.Stats().GoodNodes > 0 {
				r.FirstNodeMs = ms(clock.Since(start))
			}
		case <-bctx.Done():
			// Give BootstrapContext a moment to return.
			select {
			case bootErr = <-boot:
			case <-time.After(500 * time.Millisecond):
			}
			break wait
		}
	}
	r.BootstrapMs = ms(clock.Since(start))
	st := s.Stats()
	r.Nodes, r.GoodNodes = st.Nodes, st.GoodNodes
	if r.FirstNodeMs == 0 && r.GoodNodes > 0 {
		r.FirstNodeMs = r.BootstrapMs
	}
	if r.Nodes == 0 {
		if bootErr != nil && ctx.Err() == nil {
			r.Err = "bootstrap: " + bootErr.Error()
		} else {
			r.Err = "no DHT node answered within the wait time (UDP to the bootstrap routers blocked?)"
		}
		return r
	}

	// BEP 44 mutable put and get of a random throwaway item.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	var key [32]byte
	copy(key[:], pub)
	salt := make([]byte, 16)
	rand.Read(salt)
	var rnd [8]byte
	rand.Read(rnd[:])
	value := "lanfolk-natprobe-" + hex.EncodeToString(rnd[:])
	target := bep44.MakeMutableTarget(key, salt)

	pctx, pcancel := context.WithTimeout(ctx, 25*time.Second)
	defer pcancel()
	t0 := clock.Nanotime()
	stats, err := getput.Put(pctx, target, s, salt, func(seq int64) bep44.Put {
		p := bep44.Put{V: value, K: &key, Salt: salt, Seq: seq + 1}
		p.Sign(priv)
		return p
	})
	r.PutMs = ms(clock.Since(t0))
	if stats != nil {
		r.PutAnswers = stats.NumResponses
	}
	if err != nil {
		r.Err = "put: " + err.Error()
		return r
	}
	if r.PutAnswers == 0 {
		r.Err = "put: no node answered the traversal"
		return r
	}
	// getput.Put does not report per-node store results, so PutOK means the
	// traversal ran and store requests went out; the get below confirms it.
	r.PutOK = true

	gctx, gcancel := context.WithTimeout(ctx, 25*time.Second)
	defer gcancel()
	t1 := clock.Nanotime()
	res, _, err := getput.Get(gctx, target, s, nil, salt)
	r.GetMs = ms(clock.Since(t1))
	switch {
	case err != nil:
		r.Err = "get: " + err.Error()
	case !res.Mutable || string(res.V) != fmt.Sprintf("%d:%s", len(value), value):
		r.Err = "get returned a different value"
	default:
		r.GetOK = true
	}
	return r
}
