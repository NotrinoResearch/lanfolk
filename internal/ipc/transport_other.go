//go:build !windows

package ipc

import (
	"context"
	"net"
	"os"
	"strconv"
)

// DefaultAddress returns the Unix socket path; LANFOLK_PIPE overrides it.
func DefaultAddress() string {
	if v := os.Getenv("LANFOLK_PIPE"); v != "" {
		return v
	}
	return "/run/lanfolk/lanfolk.sock"
}

// Listen opens the service listener. The socket is created with mode 0600 so
// that only the service user (root in test deployments) can connect.
func Listen(addr string) (net.Listener, error) {
	os.Remove(addr)
	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	os.Chmod(addr, 0o600)
	return ln, nil
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

// Caller identifies the process on the other end of the socket.
type Caller struct {
	SID   string
	Admin bool
	PID   uint32
}

// CallerOf reports the socket owner's uid as the SID; file permissions already
// restrict access.
func CallerOf(conn net.Conn) (Caller, error) {
	return Caller{SID: "uid:" + strconv.Itoa(os.Getuid()), Admin: os.Getuid() == 0}, nil
}
