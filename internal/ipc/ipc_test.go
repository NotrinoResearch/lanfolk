package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testAddr(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\lanfolk-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	return filepath.Join(t.TempDir(), "l.sock")
}

func startServer(t *testing.T, h Handler, auth func(net.Conn) error) (*Server, string) {
	addr := testAddr(t)
	ln, err := Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Handler: h, Authorize: auth}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, addr
}

func TestCallsErrorsAndEvents(t *testing.T) {
	s, addr := startServer(t, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case MethodAddFriend:
			var p AddFriendParams
			if err := Decode(params, &p); err != nil {
				return nil, err
			}
			if p.Code == "" {
				return nil, Errorf(CodeInvalid, "friend code is required")
			}
			return Friend{FriendCode: p.Code, State: "pending"}, nil
		case MethodStatus:
			return Status{Version: "test"}, nil
		case "boom":
			panic("handler bug")
		}
		return nil, Errorf(CodeInvalid, "unknown method %q", method)
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialAddr(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var st Status
	if err := c.Call(ctx, MethodStatus, nil, &st); err != nil || st.Version != "test" {
		t.Fatalf("status: %v %+v", err, st)
	}
	var f Friend
	if err := c.Call(ctx, MethodAddFriend, AddFriendParams{Code: "lanfolk1x"}, &f); err != nil || f.State != "pending" {
		t.Fatalf("add friend: %v", err)
	}
	err = c.Call(ctx, MethodAddFriend, AddFriendParams{}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInvalid {
		t.Fatalf("expected invalid error, got %v", err)
	}
	if err := c.Call(ctx, "boom", nil, nil); !errors.As(err, &e) || e.Code != CodeInternal {
		t.Fatalf("panic not converted: %v", err)
	}
	// The connection survives a handler panic.
	if err := c.Call(ctx, MethodStatus, nil, &st); err != nil {
		t.Fatal(err)
	}

	if err := c.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	s.Publish(EventPeers, []Peer{{Nickname: "Veli", Path: "direct"}})
	select {
	case m := <-c.Events():
		var peers []Peer
		json.Unmarshal(m.Data, &peers)
		if m.Event != EventPeers || len(peers) != 1 || peers[0].Nickname != "Veli" {
			t.Fatalf("event %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
}

func TestAuthorizeRejects(t *testing.T) {
	_, addr := startServer(t, func(context.Context, string, json.RawMessage) (any, error) { return true, nil },
		func(net.Conn) error { return errors.New("not the session owner") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialAddr(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(ctx, MethodStatus, nil, nil)
	if err == nil {
		t.Fatal("unauthorized client served")
	}
}

func TestCallerOfIdentifiesClient(t *testing.T) {
	got := make(chan Caller, 1)
	_, addr := startServer(t, func(context.Context, string, json.RawMessage) (any, error) { return true, nil },
		func(c net.Conn) error {
			p, err := CallerOf(c)
			if err != nil {
				return err
			}
			got <- p
			return nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialAddr(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(ctx, MethodStatus, nil, nil); err != nil {
		t.Fatal(err)
	}
	p := <-got
	if p.SID == "" {
		t.Fatal("no SID")
	}
}
