package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Request is one call.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Error is a structured error returned to clients.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf creates an Error with a code.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Message is a response or an event.
type Message struct {
	ID     uint64          `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// MaxMessage bounds one line of the protocol.
const MaxMessage = 1 << 20

// Handler serves one request. Returning an *Error keeps its code; other
// errors become CodeInternal.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Server accepts clients on a listener.
type Server struct {
	Handler Handler
	// Authorize is called for every new connection before any request is
	// served; returning an error closes the connection.
	Authorize func(conn net.Conn) error
	Logger    *slog.Logger

	mu      sync.Mutex
	clients map[*serverConn]struct{}
}

type serverConn struct {
	conn       net.Conn
	writeMu    sync.Mutex
	enc        *json.Encoder
	subscribed atomic.Bool

	// Events wait here for the connection's writer goroutine, so Publish
	// never blocks on a slow or frozen client. Every event is a snapshot of
	// state, so a newer one replaces an unsent one of the same name.
	evMu    sync.Mutex
	evData  map[string]json.RawMessage
	evOrder []string
	evKick  chan struct{}
}

// eventWriteTimeout closes a client that has not read an event for this
// long; it reconnects when it runs again.
const eventWriteTimeout = 30 * time.Second

func (c *serverConn) queueEvent(name string, data json.RawMessage) {
	c.evMu.Lock()
	if _, ok := c.evData[name]; !ok {
		c.evOrder = append(c.evOrder, name)
	}
	c.evData[name] = data
	c.evMu.Unlock()
	select {
	case c.evKick <- struct{}{}:
	default:
	}
}

func (c *serverConn) writeEvents(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-c.evKick:
		}
		c.evMu.Lock()
		order, data := c.evOrder, c.evData
		c.evOrder, c.evData = nil, make(map[string]json.RawMessage, len(data))
		c.evMu.Unlock()
		for _, name := range order {
			c.conn.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
			err := c.send(&Message{Event: name, Data: data[name]})
			c.conn.SetWriteDeadline(time.Time{})
			if err != nil {
				c.conn.Close()
				return
			}
		}
	}
}

func (c *serverConn) send(m *Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(m)
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Logger == nil {
		s.Logger = slog.New(slog.DiscardHandler)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				s.closeAll()
				return nil
			}
			continue
		}
		go s.handle(ctx, c)
	}
}

func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.conn.Close()
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if s.Authorize != nil {
		if err := s.Authorize(conn); err != nil {
			s.Logger.Warn("rejected control client", "err", err)
			json.NewEncoder(conn).Encode(&Message{Error: &Error{Code: CodeUnauthorized, Message: err.Error()}})
			return
		}
	}
	sc := &serverConn{conn: conn, enc: json.NewEncoder(conn),
		evData: make(map[string]json.RawMessage), evKick: make(chan struct{}, 1)}
	done := make(chan struct{})
	defer close(done)
	s.mu.Lock()
	if s.clients == nil {
		s.clients = make(map[*serverConn]struct{})
	}
	s.clients[sc] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, sc)
		s.mu.Unlock()
	}()
	sc2 := bufio.NewScanner(conn)
	sc2.Buffer(make([]byte, 64*1024), MaxMessage)
	for sc2.Scan() {
		var req Request
		if err := json.Unmarshal(sc2.Bytes(), &req); err != nil {
			sc.send(&Message{Error: &Error{Code: CodeInvalid, Message: "malformed request"}})
			continue
		}
		if req.Method == MethodSubscribe {
			if !sc.subscribed.Swap(true) {
				go sc.writeEvents(done)
			}
			sc.send(&Message{ID: req.ID, Result: json.RawMessage("true")})
			continue
		}
		res, err := s.call(ctx, req)
		msg := &Message{ID: req.ID}
		if err != nil {
			var e *Error
			if errors.As(err, &e) {
				msg.Error = e
			} else {
				msg.Error = &Error{Code: CodeInternal, Message: err.Error()}
			}
		} else {
			b, merr := json.Marshal(res)
			if merr != nil {
				msg.Error = &Error{Code: CodeInternal, Message: merr.Error()}
			} else {
				msg.Result = b
			}
		}
		if err := sc.send(msg); err != nil {
			return
		}
	}
}

func (s *Server) call(ctx context.Context, req Request) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.Logger.Error("control handler panic", "method", req.Method, "panic", r)
			err = Errorf(CodeInternal, "internal error")
		}
	}()
	if s.Handler == nil {
		return nil, Errorf(CodeInternal, "no handler")
	}
	return s.Handler(ctx, req.Method, req.Params)
}

// Publish queues an event for every subscribed client. It never blocks on
// a client and encodes nothing when no client is subscribed.
func (s *Server) Publish(event string, data any) {
	s.mu.Lock()
	clients := make([]*serverConn, 0, len(s.clients))
	for c := range s.clients {
		if c.subscribed.Load() {
			clients = append(clients, c)
		}
	}
	s.mu.Unlock()
	if len(clients) == 0 {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	for _, c := range clients {
		c.queueEvent(event, b)
	}
}

// HasSubscribers reports whether any client listens for events, so callers
// can skip building them.
func (s *Server) HasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if c.subscribed.Load() {
			return true
		}
	}
	return false
}

// Client is a connection to the service.
type Client struct {
	conn    net.Conn
	enc     *json.Encoder
	writeMu sync.Mutex
	nextID  atomic.Uint64
	mu      sync.Mutex
	pending map[uint64]chan *Message
	events  chan *Message
	done    chan struct{}
	err     error
}

// Dial connects to the service at the platform default address.
func Dial(ctx context.Context) (*Client, error) { return DialAddr(ctx, DefaultAddress()) }

// DialAddr connects to the service at addr.
func DialAddr(ctx context.Context, addr string) (*Client, error) {
	conn, err := dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	return NewClient(conn), nil
}

// NewClient wraps an existing connection.
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, enc: json.NewEncoder(conn), pending: make(map[uint64]chan *Message),
		events: make(chan *Message, 64), done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer close(c.done)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64*1024), MaxMessage)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Event != "" {
			select {
			case c.events <- &m:
			default: // a slow consumer loses old updates, never blocks replies
			}
			continue
		}
		if m.ID == 0 && m.Error != nil {
			c.err = m.Error
			break
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	}
	if c.err == nil {
		c.err = errors.New("connection to the Lanfolk service closed")
	}
	c.mu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.events)
}

// Call invokes method and decodes the result into out (may be nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	req := Request{ID: id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	ch := make(chan *Message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.err
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	c.writeMu.Lock()
	err := c.enc.Encode(&req)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return c.err
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Subscribe asks for events; read them from Events.
func (c *Client) Subscribe(ctx context.Context) error { return c.Call(ctx, MethodSubscribe, nil, nil) }

// Events delivers pushed events. The channel closes with the connection.
func (c *Client) Events() <-chan *Message { return c.events }

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Decode unmarshals params into v, returning an invalid-parameters error.
func Decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return Errorf(CodeInvalid, "missing parameters")
	}
	if err := json.Unmarshal(params, v); err != nil {
		return Errorf(CodeInvalid, "invalid parameters: %v", err)
	}
	return nil
}
