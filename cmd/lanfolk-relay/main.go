// Command lanfolk-relay runs the shared Lanfolk relay: STUN, signaling and
// the final fallback data path. It is configured with environment variables
// so that it fits Docker deployments; see deploy/relay and docs/SELF_HOST.md.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/relay"
	"github.com/NotrinoResearch/lanfolk/internal/version"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "lanfolk-relay:", err)
			os.Exit(1)
		}
	case "healthcheck":
		os.Exit(healthcheck())
	case "version":
		fmt.Println(version.Version)
	default:
		fmt.Fprintf(os.Stderr, "usage: lanfolk-relay [serve|healthcheck|version]\n")
		os.Exit(2)
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return n, nil
}

func config() (relay.Config, error) {
	cfg := relay.DefaultConfig()
	cfg.PublicHost = env("RELAY_PUBLIC_HOST", "")
	udpPort, err := envInt("RELAY_UDP_PORT", 3478)
	if err != nil {
		return cfg, err
	}
	tcpPort, err := envInt("RELAY_TCP_PORT", 443)
	if err != nil {
		return cfg, err
	}
	bind := env("RELAY_BIND_ADDR", "")
	cfg.UDPAddr = net.JoinHostPort(bind, strconv.Itoa(udpPort))
	if tcpPort == 0 {
		cfg.TCPAddr = ""
	} else {
		cfg.TCPAddr = net.JoinHostPort(bind, strconv.Itoa(tcpPort))
	}
	if cfg.MaxSessionKbps, err = envInt("RELAY_MAX_SESSION_KBPS", cfg.MaxSessionKbps); err != nil {
		return cfg, err
	}
	if cfg.MaxSessionsPerIP, err = envInt("RELAY_MAX_SESSIONS_PER_IP", cfg.MaxSessionsPerIP); err != nil {
		return cfg, err
	}
	if cfg.MaxSessions, err = envInt("RELAY_MAX_SESSIONS", cfg.MaxSessions); err != nil {
		return cfg, err
	}
	idle, err := envInt("RELAY_IDLE_TIMEOUT_SEC", int(cfg.IdleTimeout/time.Second))
	if err != nil {
		return cfg, err
	}
	cfg.IdleTimeout = time.Duration(idle) * time.Second
	cfg.MetricsAddr = env("RELAY_METRICS_ADDR", cfg.MetricsAddr)
	if cfg.MetricsAddr == "off" {
		cfg.MetricsAddr = ""
	}
	return cfg, nil
}

func serve() error {
	level := slog.LevelInfo
	if os.Getenv("RELAY_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	cfg, err := config()
	if err != nil {
		return err
	}
	cfg.Logger = log
	srv, err := relay.New(cfg)
	if err != nil {
		return err
	}
	log.Info("lanfolk-relay", "version", version.Version, "public_host_set", cfg.PublicHost != "")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx)
}

// healthcheck is used by the Docker HEALTHCHECK; the image has no shell or
// curl, so the binary checks its own metrics endpoint.
func healthcheck() int {
	addr := env("RELAY_METRICS_ADDR", relay.DefaultConfig().MetricsAddr)
	if addr == "off" {
		return 0
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy: status", resp.StatusCode)
		return 1
	}
	return 0
}
