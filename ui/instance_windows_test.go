//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The helper process plays a running Lanfolk: it creates the single instance
// lock window and a main window, then either handles messages ("alive") or
// never does again ("hung").
func TestHelperInstanceWindows(t *testing.T) {
	mode := os.Getenv("LANFOLK_TEST_INSTANCE")
	if mode == "" {
		t.Skip("helper process only")
	}
	runtime.LockOSThread()
	var (
		register = user32.NewProc("RegisterClassExW")
		create   = user32.NewProc("CreateWindowExW")
		defProc  = user32.NewProc("DefWindowProcW")
		getMsg   = user32.NewProc("GetMessageW")
		dispatch = user32.NewProc("DispatchMessageW")
	)
	wndProc := syscall.NewCallback(func(h, msg, w, l uintptr) uintptr {
		r, _, _ := defProc.Call(h, msg, w, l)
		return r
	})
	type wndClassEx struct {
		size, style                        uint32
		wndProc                            uintptr
		clsExtra, wndExtra                 int32
		instance, icon, cursor, background windows.Handle
		menuName, className                *uint16
		iconSm                             windows.Handle
	}
	window := func(class, name string) {
		c, _ := windows.UTF16PtrFromString(class)
		n, _ := windows.UTF16PtrFromString(name)
		wc := wndClassEx{wndProc: wndProc, className: c}
		wc.size = uint32(unsafe.Sizeof(wc))
		register.Call(uintptr(unsafe.Pointer(&wc)))
		if h, _, err := create.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(n)), 0, 0, 0, 0, 0, 0, 0, 0, 0); h == 0 {
			fmt.Println("create failed:", err)
			os.Exit(2)
		}
	}
	lockClass, lockName := singleInstanceWindow(os.Getenv("LANFOLK_TEST_ID"))
	window(lockClass, lockName)
	window(os.Getenv("LANFOLK_TEST_CLASS"), "main")
	fmt.Println("ready")
	if mode == "hung" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	var msg [48]byte
	for {
		if r, _, _ := getMsg.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); r == 0 || int32(r) == -1 {
			os.Exit(0)
		}
		dispatch.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}

// startInstance runs the helper and waits until its windows exist.
func startInstance(t *testing.T, mode string) (id, class string, cmd *exec.Cmd, exited chan error) {
	t.Helper()
	id = fmt.Sprintf("lanfolk-test-%d-%s", time.Now().UnixNano(), mode)
	class = "LanfolkTestWindow" + id
	cmd = exec.Command(os.Args[0], "-test.run=^TestHelperInstanceWindows$")
	cmd.Env = append(os.Environ(), "LANFOLK_TEST_INSTANCE="+mode, "LANFOLK_TEST_ID="+id, "LANFOLK_TEST_CLASS="+class)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited = make(chan error, 1)
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		ok := false
		for sc.Scan() {
			if sc.Text() == "ready" && !ok {
				ok = true
				ready <- true
			}
		}
		if !ok {
			ready <- false
		}
		exited <- cmd.Wait()
	}()
	t.Cleanup(func() { cmd.Process.Kill() })
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("helper did not create its windows")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("helper did not start")
	}
	return id, class, cmd, exited
}

func shortHungTimeout(t *testing.T) {
	old := hungTimeout
	hungTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hungTimeout = old })
}

func TestEndHungInstanceEndsAnInstanceThatDoesNotRespond(t *testing.T) {
	shortHungTimeout(t)
	id, class, _, exited := startInstance(t, "hung")
	if !endHungInstance(id, class) {
		t.Fatal("hung instance not ended")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("hung instance still running")
	}
	if endHungInstance(id, class) {
		t.Fatal("ended an instance that is gone")
	}
}

func TestEndHungInstanceKeepsAResponsiveInstance(t *testing.T) {
	shortHungTimeout(t)
	id, class, _, exited := startInstance(t, "alive")
	start := time.Now()
	if endHungInstance(id, class) {
		t.Fatal("responsive instance ended")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("check took %v", d)
	}
	select {
	case err := <-exited:
		t.Fatalf("responsive instance exited: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEndHungInstanceWithoutARunningInstance(t *testing.T) {
	if endHungInstance(fmt.Sprintf("lanfolk-test-none-%d", time.Now().UnixNano()), "LanfolkTestNone") {
		t.Fatal("ended something without a running instance")
	}
}
