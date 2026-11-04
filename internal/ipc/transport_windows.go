//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"

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

// pipeClientAccess is what interactive users may do with the pipe: read,
// write data and attributes. It deliberately leaves out FILE_APPEND_DATA,
// which on a pipe means FILE_CREATE_PIPE_INSTANCE: with it (it is part of
// GENERIC_WRITE) any local user could add a server instance of the service's
// pipe and receive the next client's requests.
const pipeClientAccess = windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA | windows.FILE_WRITE_ATTRIBUTES

// pipeSDDL grants full access to LocalSystem and Administrators and
// pipeClientAccess (0x12019b) to interactive users. The service additionally
// checks that the connecting user is the session owner (see Authorize).
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;IU)"

// listenSDDL is pipeSDDL for the service (LocalSystem). A listener that runs
// as an ordinary user (tests, the UI's -mock service, a development run)
// also needs the right to create the pipe instances it accepts on, so its own
// user is added; other users still get pipeClientAccess only.
func listenSDDL() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return pipeSDDL
	}
	sid := u.User.Sid.String()
	if sid == "S-1-5-18" {
		return pipeSDDL
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")(A;;0x12019b;;;IU)"
}

// Listen opens the service listener.
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, &winio.PipeConfig{
		SecurityDescriptor: listenSDDL(),
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
}

// trustedPipeOwners may own the service's pipe: the service runs as
// LocalSystem, whose objects are owned by LocalSystem or Administrators. A
// standard user cannot create an object with either owner.
var trustedPipeOwners = []string{"S-1-5-18", "S-1-5-32-544"}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	// Identification level: the service can learn who we are, but cannot
	// act as us.
	c, err := winio.DialPipeAccessImpLevel(ctx, addr, pipeClientAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("the Lanfolk service is not running")
		}
		return nil, err
	}
	// The real pipe is created by the service. A pipe of the same name made
	// by another local user while the service was stopped has that user as
	// owner; never send it anything (it could ask for a backup password).
	if addr == PipeName {
		if err := checkPipeOwner(c); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func checkPipeOwner(conn net.Conn) error {
	fc, ok := conn.(fdConn)
	if !ok {
		return errors.New("connection has no pipe handle")
	}
	owner, err := handleOwner(windows.Handle(fc.Fd()))
	if err != nil {
		return fmt.Errorf("check the Lanfolk service pipe: %w", err)
	}
	for _, t := range trustedPipeOwners {
		if owner == t {
			return nil
		}
	}
	return fmt.Errorf("the Lanfolk control pipe is owned by %s, not by the service; another program may be imitating Lanfolk", owner)
}

func handleOwner(h windows.Handle) (string, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	o, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	if o == nil {
		return "", errors.New("pipe has no owner")
	}
	return o.String(), nil
}

// Caller identifies the process on the other end of a pipe.
type Caller struct {
	SID   string
	Admin bool
	PID   uint32
}

type fdConn interface{ Fd() uintptr }

var procImpersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

// CallerOf returns the identity of the pipe client. The user and elevation
// come from the client's own token, obtained by impersonation, so they
// cannot be confused with another process that later reuses the client's
// process ID. Windows allows this only after data was read from the pipe
// (the server calls Authorize after the first request line). PID is for
// logs only.
func CallerOf(conn net.Conn) (Caller, error) {
	var p Caller
	fc, ok := conn.(fdConn)
	if !ok {
		return p, errors.New("connection has no pipe handle")
	}
	h := windows.Handle(fc.Fd())
	windows.GetNamedPipeClientProcessId(h, &p.PID)

	type result struct {
		c   Caller
		err error
	}
	ch := make(chan result, 1)
	// Impersonation changes the identity of the OS thread. It runs on a
	// thread of its own: if reverting fails, the goroutine exits with the
	// thread still locked and the runtime discards that thread.
	go func() {
		runtime.LockOSThread()
		var r result
		if ok, _, e := procImpersonateNamedPipeClient.Call(uintptr(h)); ok == 0 {
			r.err = fmt.Errorf("identify client: %w", e)
			runtime.UnlockOSThread()
			ch <- r
			return
		}
		var tok windows.Token
		err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok)
		if err == nil {
			if u, uerr := tok.GetTokenUser(); uerr == nil {
				r.c.SID = u.User.Sid.String()
				r.c.Admin = tok.IsElevated()
			} else {
				err = uerr
			}
			tok.Close()
		}
		if err != nil {
			r.err = fmt.Errorf("client token: %w", err)
		}
		if windows.RevertToSelf() != nil {
			ch <- result{err: errors.New("could not stop impersonating the client")}
			return // thread stays locked and is discarded
		}
		runtime.UnlockOSThread()
		ch <- r
	}()
	r := <-ch
	if r.err != nil {
		return p, r.err
	}
	r.c.PID = p.PID
	return r.c, nil
}
