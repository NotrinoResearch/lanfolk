// Command lanfolk-service runs the Lanfolk client: the virtual adapter,
// tunnel, discovery and path selection. On Windows it is a service
// (`install`, `uninstall`, `start`, `stop`, or `run` in the foreground);
// elsewhere `run` starts it in the foreground (Linux test clients).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/NotrinoResearch/lanfolk/internal/engine"
	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/version"
	"github.com/NotrinoResearch/lanfolk/internal/winnet"
)

type options struct {
	dataDir  string
	pipe     string
	verbose  bool
	noTUN    bool
	listen   string
	loopback bool
	relay    string
}

func main() {
	if len(os.Args) < 2 {
		if isService() {
			if err := runService(); err != nil {
				os.Exit(1)
			}
			return
		}
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	o := options{}
	fs.StringVar(&o.dataDir, "data-dir", defaultDataDir(), "data directory")
	fs.StringVar(&o.pipe, "pipe", ipc.DefaultAddress(), "control pipe or socket")
	fs.BoolVar(&o.verbose, "v", false, "verbose logging")
	fs.BoolVar(&o.noTUN, "no-tun", false, "run without a virtual adapter (control plane only)")
	fs.StringVar(&o.listen, "listen", "", "UDP listen address (development; default: all interfaces)")
	fs.BoolVar(&o.loopback, "loopback-candidates", false, "advertise 127.0.0.1 (development on one machine)")
	fs.StringVar(&o.relay, "relay", "", "default relay host:udp[:tcp], overriding the built-in one")
	fs.Parse(args)
	var err error
	switch cmd {
	case "run":
		err = runForeground(o)
	case "install":
		err = install(o)
	case "uninstall":
		err = uninstall()
	case "start":
		err = startService()
	case "stop":
		err = stopService()
	case "version":
		fmt.Println(version.Version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanfolk-service:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: lanfolk-service <command> [flags]

commands:
  run         run in the foreground (logs to the console)
  install     register the Windows service and firewall rules (administrator)
  uninstall   remove the service, firewall rules and adapter (administrator)
  start       start the service
  stop        stop the service
  version     print the version`)
}

func defaultDataDir() string {
	if v := os.Getenv("LANFOLK_DATA_DIR"); v != "" {
		return v
	}
	return platformDataDir()
}

// rotatingFile is a size-bounded log file (keeps two old files).
type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

const maxLogSize = 5 << 20

func openLog(dir string) (*rotatingFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &rotatingFile{path: filepath.Join(dir, "service.log")}
	return r, r.open()
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > maxLogSize {
		r.f.Close()
		os.Remove(r.path + ".2")
		os.Rename(r.path+".1", r.path+".2")
		os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func newLogger(o options, console bool) (*slog.Logger, error) {
	lf, err := openLog(filepath.Join(o.dataDir, "logs"))
	if err != nil {
		return nil, err
	}
	var w io.Writer = lf
	if console {
		w = io.MultiWriter(lf, os.Stderr)
	}
	level := slog.LevelInfo
	if o.verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), nil
}

// serve runs the engine and the control server until ctx ends.
func serve(ctx context.Context, o options, log *slog.Logger) error {
	srv := &ipc.Server{Logger: log}
	cfg := engine.Config{
		DataDir:    o.dataDir,
		Logger:     log,
		Blocked:    winnet.IsBlocked,
		OnEvent:    srv.Publish,
		WantEvents: srv.HasSubscribers,
	}
	cfg.ListenAddr = o.listen
	cfg.LoopbackCandidates = o.loopback
	if o.relay != "" {
		r, err := parseRelayFlag(o.relay)
		if err != nil {
			return err
		}
		cfg.DefaultRelays = []store.Relay{r}
	}
	if !o.noTUN {
		cfg.NewTUN = createTUN
		cfg.NewAdapter = func(dev tunDevice) (engine.Adapter, error) { return winnet.NewAdapter(dev) }
		cfg.StartCapture = startCapture
	}
	e, err := engine.New(cfg)
	if err != nil {
		return err
	}
	srv.Handler = e.Handle
	srv.Authorize = func(c net.Conn) error { return authorize(e, c) }
	ln, err := ipc.Listen(o.pipe)
	if err != nil {
		return fmt.Errorf("open control pipe: %w", err)
	}
	log.Info("lanfolk service started", "version", version.Version, "key", e.PublicKey().String(), "udp_port", e.Port())
	go srv.Serve(ctx, ln)
	return e.Run(ctx)
}

// authorize lets the session owner and administrators control the service.
// The owner is recorded at install time; if none is recorded, the first
// client becomes the owner.
func authorize(e *engine.Engine, c net.Conn) error {
	caller, err := ipc.CallerOf(c)
	if err != nil {
		return err
	}
	if caller.Admin {
		return nil
	}
	ok, err := e.ClaimOwner(caller.SID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("only the user who installed Lanfolk can control it")
	}
	return nil
}

func parseRelayFlag(s string) (store.Relay, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return store.Relay{}, fmt.Errorf("-relay must be host:udp[:tcp]")
	}
	r := store.Relay{Host: parts[0]}
	var err error
	if r.UDPPort, err = strconv.Atoi(parts[1]); err != nil {
		return r, fmt.Errorf("-relay: bad UDP port")
	}
	if len(parts) == 3 {
		if r.TCPPort, err = strconv.Atoi(parts[2]); err != nil {
			return r, fmt.Errorf("-relay: bad TCP port")
		}
	}
	return r, nil
}

func startCapture(ctx context.Context, deliver func([]byte)) (func(), error) {
	c, err := winnet.StartBroadcastCapture(ctx, deliver)
	if err != nil {
		return nil, err
	}
	return func() { c.Close() }, nil
}
