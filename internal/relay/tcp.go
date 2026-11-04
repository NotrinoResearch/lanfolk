package relay

import (
	"bufio"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/wire"
)

// tcpConn is one TCP fallback client. Frames are length-prefixed; see
// wire.AppendStreamFrame. Outgoing frames go through a short queue and are
// dropped when it is full: a late game packet is worse than a lost one.
type tcpConn struct {
	conn     net.Conn
	remote   netip.AddrPort
	out      chan []byte
	once     sync.Once
	closed   chan struct{}
	sessions map[wire.SessionID]struct{} // guarded by Server.mu
	srv      *Server

	// nsess mirrors len(sessions) for readers without Server.mu.
	nsess atomic.Int32
	// lastActive is the time of the last frame received, or of the removal
	// of the last session (unix nanoseconds).
	lastActive atomic.Int64
}

const tcpQueueLen = 128

func (s *Server) acceptLoop() {
	for {
		c, err := s.tcpLn.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		remote := c.RemoteAddr().(*net.TCPAddr).AddrPort()
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		key := limitKey(remote.Addr())
		s.tcpMu.Lock()
		if s.tcpPerIP[key] >= s.cfg.MaxTCPConnsPerIP || len(s.tcpConns) >= s.cfg.MaxTCPConns {
			s.tcpMu.Unlock()
			s.metrics.rejects.Add(1)
			s.metrics.tcpRejects.Add(1)
			c.Close()
			continue
		}
		s.tcpPerIP[key]++
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
		}
		t := &tcpConn{
			conn:     c,
			remote:   remote,
			out:      make(chan []byte, tcpQueueLen),
			closed:   make(chan struct{}),
			sessions: make(map[wire.SessionID]struct{}),
			srv:      s,
		}
		t.lastActive.Store(time.Now().UnixNano())
		s.tcpConns[t] = struct{}{}
		s.tcpMu.Unlock()
		s.metrics.tcpConns.Add(1)
		go t.writeLoop()
		go t.readLoop()
	}
}

func (t *tcpConn) readLoop() {
	defer t.close()
	r := bufio.NewReaderSize(t.conn, 16*1024)
	buf := make([]byte, wire.MaxStreamFrame)
	for {
		t.conn.SetReadDeadline(time.Now().Add(t.srv.cfg.IdleTimeout))
		f, err := wire.ReadStreamFrame(r, buf)
		if err != nil {
			return
		}
		t.srv.metrics.tcpFrames.Add(1)
		t.lastActive.Store(time.Now().UnixNano())
		t.srv.handle(f, endpoint{tcp: t})
	}
}

func (t *tcpConn) writeLoop() {
	w := bufio.NewWriterSize(t.conn, 16*1024)
	for {
		select {
		case <-t.closed:
			return
		case f := <-t.out:
			t.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := w.Write(f); err != nil {
				t.close()
				return
			}
			// Flush when the queue is drained so bursts share one segment
			// without delaying a lone packet.
			if len(t.out) == 0 {
				if err := w.Flush(); err != nil {
					t.close()
					return
				}
			}
		}
	}
}

func (t *tcpConn) send(frame []byte) {
	select {
	case <-t.closed:
		return
	default:
	}
	b := wire.AppendStreamFrame(make([]byte, 0, len(frame)+2), frame)
	select {
	case t.out <- b:
	default:
		t.srv.metrics.droppedQueue.Add(1)
	}
}

func (t *tcpConn) close() {
	t.once.Do(func() {
		close(t.closed)
		t.conn.Close()
		s := t.srv
		s.mu.Lock()
		for sid := range t.sessions {
			if sess := s.sessions[sid]; sess != nil {
				for i, p := range sess.parties {
					if p != nil && p.ep.tcp == t {
						s.removePartyLocked(sess, i)
					}
				}
			}
		}
		s.mu.Unlock()
		key := limitKey(t.remote.Addr())
		s.tcpMu.Lock()
		if s.tcpPerIP[key] <= 1 {
			delete(s.tcpPerIP, key)
		} else {
			s.tcpPerIP[key]--
		}
		delete(s.tcpConns, t)
		s.tcpMu.Unlock()
		s.metrics.tcpConns.Add(-1)
	})
}

// closeIfIdle closes the connection if it holds no session and has been
// silent for the TCP grace time (relay hardening R3): such a connection only
// takes a slot that a real client may need. Clients ping every 5 s, so a
// client without sessions keeps its connection.
func (t *tcpConn) closeIfIdle(now int64) {
	if t.nsess.Load() != 0 {
		return
	}
	if now-t.lastActive.Load() > int64(t.srv.cfg.TCPGrace) {
		t.srv.metrics.tcpIdleClosed.Add(1)
		t.close()
	}
}

func (s *Server) closeAllTCP() {
	s.tcpMu.Lock()
	conns := make([]*tcpConn, 0, len(s.tcpConns))
	for c := range s.tcpConns {
		conns = append(conns, c)
	}
	s.tcpMu.Unlock()
	for _, c := range conns {
		c.close()
	}
}
