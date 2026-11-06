package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type metrics struct {
	udpPackets          atomic.Uint64
	udpBytes            atomic.Uint64
	tcpFrames           atomic.Uint64
	tcpConns            atomic.Int64
	forwardedPackets    atomic.Uint64
	forwardedBytes      atomic.Uint64
	stunRequests        atomic.Uint64
	registrations       atomic.Uint64
	rejects             atomic.Uint64
	expired             atomic.Uint64
	takeovers           atomic.Uint64
	droppedRate         atomic.Uint64
	droppedNoPeer       atomic.Uint64
	droppedUnregistered atomic.Uint64
	droppedInvalid      atomic.Uint64
	droppedQueue        atomic.Uint64
	droppedIPRate       atomic.Uint64
	droppedTotalRate    atomic.Uint64
	challenges          atomic.Uint64
	cookieRejects       atomic.Uint64
	tcpRejects          atomic.Uint64
	tcpIdleClosed       atomic.Uint64
	echoRejects         atomic.Uint64
}

func (s *Server) writeMetrics(w http.ResponseWriter) {
	st := s.Status()
	m := &s.metrics
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	line := func(name, typ, help string, v any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, v)
	}
	line("lanfolk_relay_sessions", "gauge", "Active relay sessions.", st.Sessions)
	line("lanfolk_relay_parties", "gauge", "Registered session parties.", st.Parties)
	line("lanfolk_relay_tcp_connections", "gauge", "Open TCP fallback connections.", st.TCPConns)
	line("lanfolk_relay_pending_echoes", "gauge", "Scheduled delayed echo replies.", st.Echoes)
	line("lanfolk_relay_udp_packets_total", "counter", "UDP datagrams received.", m.udpPackets.Load())
	line("lanfolk_relay_udp_bytes_total", "counter", "UDP bytes received.", m.udpBytes.Load())
	line("lanfolk_relay_tcp_frames_total", "counter", "TCP frames received.", m.tcpFrames.Load())
	line("lanfolk_relay_forwarded_packets_total", "counter", "Packets forwarded between parties.", m.forwardedPackets.Load())
	line("lanfolk_relay_forwarded_bytes_total", "counter", "Bytes forwarded between parties.", m.forwardedBytes.Load())
	line("lanfolk_relay_stun_requests_total", "counter", "STUN binding requests answered.", m.stunRequests.Load())
	line("lanfolk_relay_registrations_total", "counter", "Session registrations accepted.", m.registrations.Load())
	line("lanfolk_relay_rejects_total", "counter", "Rejected registrations or connections.", m.rejects.Load())
	line("lanfolk_relay_expired_parties_total", "counter", "Parties removed after the idle timeout.", m.expired.Load())
	line("lanfolk_relay_takeovers_total", "counter", "Parties taken over by a new run of the client at the same address.", m.takeovers.Load())
	line("lanfolk_relay_dropped_rate_total", "counter", "Packets dropped by rate limits.", m.droppedRate.Load())
	line("lanfolk_relay_dropped_no_peer_total", "counter", "Data packets without a second party.", m.droppedNoPeer.Load())
	line("lanfolk_relay_dropped_unregistered_total", "counter", "Data packets from unregistered sources.", m.droppedUnregistered.Load())
	line("lanfolk_relay_dropped_invalid_total", "counter", "Malformed or unknown packets.", m.droppedInvalid.Load())
	line("lanfolk_relay_dropped_queue_total", "counter", "Packets dropped on full TCP queues.", m.droppedQueue.Load())
	line("lanfolk_relay_dropped_ip_rate_total", "counter", "Data packets dropped by the per-IP cap.", m.droppedIPRate.Load())
	line("lanfolk_relay_dropped_total_rate_total", "counter", "Data packets dropped by the total cap.", m.droppedTotalRate.Load())
	line("lanfolk_relay_challenges_total", "counter", "Register cookies requested under pressure.", m.challenges.Load())
	line("lanfolk_relay_cookie_rejects_total", "counter", "Registers with a wrong or expired cookie.", m.cookieRejects.Load())
	line("lanfolk_relay_tcp_rejects_total", "counter", "TCP connections refused by the per-IP or total cap.", m.tcpRejects.Load())
	line("lanfolk_relay_tcp_idle_closed_total", "counter", "TCP connections closed without a session.", m.tcpIdleClosed.Load())
	line("lanfolk_relay_echo_rejects_total", "counter", "Delayed echoes refused by the per-IP or total cap.", m.echoRejects.Load())
	pressure := 0
	if s.pressure.Load() {
		pressure = 1
	}
	line("lanfolk_relay_under_pressure", "gauge", "1 while new UDP registrations need a cookie.", pressure)
}

func (s *Server) serveHTTP(wg *sync.WaitGroup) (stop func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) { s.writeMetrics(w) })
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.Status())
	})
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	wg.Add(1)
	go func() { defer wg.Done(); hs.Serve(s.httpLn) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(ctx)
	}
}
