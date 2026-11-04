//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// PipeName is the service's named pipe.
const PipeName = `\\.\pipe\lanfolk`

// DefaultAddress returns the pipe path; LANFOLK_PIPE overrides it for tests
// and side-by-side development builds.
func DefaultAddress() string {
	if v := os.Getenv("LANFOLK_PIPE"); v != "" {
		return v
	}
	return PipeName
}

// pipeSDDL grants full access to LocalSystem and Administrators and
// read/write to interactive users. The service additionally checks that the
// connecting user is the session owner (see Authorize).
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// Listen opens the service listener.
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, addr)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("the Lanfolk service is not running")
		}
		return nil, err
	}
	return c, nil
}

// Caller identifies the process on the other end of a pipe.
type Caller struct {
	SID   string
	Admin bool
	PID   uint32
}

type fdConn interface{ Fd() uintptr }

// CallerOf returns the identity of the pipe client.
func CallerOf(conn net.Conn) (Caller, error) {
	var p Caller
	fc, ok := conn.(fdConn)
	if !ok {
		return p, errors.New("connection has no pipe handle")
	}
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fc.Fd()), &p.PID); err != nil {
		return p, fmt.Errorf("client process: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.PID)
	if err != nil {
		return p, fmt.Errorf("open client process: %w", err)
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return p, fmt.Errorf("client token: %w", err)
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return p, err
	}
	p.SID = u.User.Sid.String()
	p.Admin = tok.IsElevated()
	return p, nil
}
