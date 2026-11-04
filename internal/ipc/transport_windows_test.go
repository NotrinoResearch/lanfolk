//go:build windows

package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func currentSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// createInstance tries to add a server instance to an existing pipe, as a
// local attacker would.
func createInstance(addr string) error {
	name, _ := windows.UTF16PtrFromString(addr)
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE, windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, nil)
	if err != nil {
		return err
	}
	windows.CloseHandle(h)
	return nil
}

// Interactive users must not be able to add server instances of the
// service's pipe (FILE_CREATE_PIPE_INSTANCE is FILE_APPEND_DATA, part of
// GENERIC_WRITE).
func TestPipeInstanceSquattingDenied(t *testing.T) {
	addr := testAddr(t)
	ln, err := winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: pipeSDDL})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := createInstance(addr); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("second server instance: err = %v, want access denied", err)
	}

	// Control: the former descriptor allowed it, so this test can see the
	// difference.
	old := testAddr(t)
	oln, err := winio.ListenPipe(old, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"})
	if err != nil {
		t.Fatal(err)
	}
	defer oln.Close()
	if err := createInstance(old); err != nil {
		t.Skipf("the former descriptor did not allow a second instance here either (%v); cannot compare", err)
	}
}

func TestCallerOfUsesClientToken(t *testing.T) {
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
	if p.SID != currentSID(t) {
		t.Fatalf("caller SID %s, want %s", p.SID, currentSID(t))
	}
	if p.PID != uint32(os.Getpid()) {
		t.Errorf("caller PID %d, want %d", p.PID, os.Getpid())
	}
}

// A pipe created by an ordinary user (here: the test itself) is not the
// service's pipe.
func TestCheckPipeOwnerRejectsUserPipe(t *testing.T) {
	addr := testAddr(t)
	ln, err := Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = checkPipeOwner(c)
	if err == nil || !strings.Contains(err.Error(), currentSID(t)) {
		t.Fatalf("checkPipeOwner = %v, want rejection naming the owner", err)
	}
}

// With LANFOLK_LIVE_PIPE=1 the installed service's pipe is checked: its owner
// must be accepted.
func TestLiveServicePipeOwner(t *testing.T) {
	if os.Getenv("LANFOLK_LIVE_PIPE") != "1" {
		t.Skip("set LANFOLK_LIVE_PIPE=1 to check the installed service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := winio.DialPipeAccessImpLevel(ctx, PipeName, pipeClientAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	owner, err := handleOwner(windows.Handle(c.(fdConn).Fd()))
	t.Logf("service pipe owner: %s (err %v)", owner, err)
	if err := checkPipeOwner(c); err != nil {
		t.Fatal(err)
	}
}

func TestListenSDDL(t *testing.T) {
	got := listenSDDL()
	if !strings.Contains(got, ";;;"+currentSID(t)+")") || !strings.Contains(got, "(A;;0x12019b;;;IU)") || strings.Contains(got, "GW;;;IU") {
		t.Fatalf("listener descriptor for a user process: %s", got)
	}
	if _, err := windows.SecurityDescriptorFromString(got); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.SecurityDescriptorFromString(pipeSDDL); err != nil {
		t.Fatal(err)
	}
}
