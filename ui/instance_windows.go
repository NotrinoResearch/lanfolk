//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Starting Lanfolk while it runs shows the running window: Wails' single
// instance lock sends the running instance a message and exits. That
// message is sent with SendMessage, which waits forever when the running
// instance does not respond (seen under heavy CPU load), so the new start
// hung invisibly and Lanfolk could not be opened again. Before the lock,
// endHungInstance checks whether the running instance of the same kind
// answers within hungTimeout and ends it when it does not. The app is only a
// client of the service, so ending it loses nothing (setup does the same).

// hungTimeout is how long a running instance's windows may take to answer.
var hungTimeout = 5 * time.Second

var (
	procSendMessageTimeout = user32.NewProc("SendMessageTimeoutW")
	procIsHungAppWindow    = user32.NewProc("IsHungAppWindow")
)

// singleInstanceWindow is the hidden window Wails' lock creates for uniqueID
// (wails/v2 internal/frontend/desktop/windows/single_instance.go).
func singleInstanceWindow(uniqueID string) (class, name string) {
	id := "wails-app-" + uniqueID
	return id + "-sic", id + "-siw"
}

// endHungInstance ends a running instance with uniqueID when its lock window
// or its main window (class mainClass) does not respond. It reports whether
// an instance was ended. Only a process running this same program is ended.
func endHungInstance(uniqueID, mainClass string) bool {
	cls, name := singleInstanceWindow(uniqueID)
	lock := findWindow(cls, name)
	if lock == 0 {
		return false // not running
	}
	pid := windowPID(lock)
	if pid == 0 || pid == uint32(os.Getpid()) {
		return false
	}
	hung := !responds(lock)
	if !hung {
		// The main window of the same process; another program's window
		// with the same class (a preview build) is left alone.
		if w := findWindow(mainClass, ""); w != 0 && windowPID(w) == pid {
			hung = !responds(w)
		}
	}
	if !hung || !sameProgram(pid) {
		return false
	}
	return endProcess(pid)
}

func findWindow(class, name string) uintptr {
	c, err := windows.UTF16PtrFromString(class)
	if err != nil {
		return 0
	}
	var n *uint16
	if name != "" {
		if n, err = windows.UTF16PtrFromString(name); err != nil {
			return 0
		}
	}
	h, _, _ := procFindWindow.Call(uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(n)))
	return h
}

func windowPID(h uintptr) uint32 {
	var pid uint32
	windows.GetWindowThreadProcessId(windows.HWND(h), &pid)
	return pid
}

// responds reports whether the window's thread handles a message within
// hungTimeout. A window Windows already marks as hung fails at once.
func responds(h uintptr) bool {
	if hung, _, _ := procIsHungAppWindow.Call(h); hung != 0 {
		return false
	}
	const wmNull, smtoAbortIfHung = 0x0000, 0x0002
	var result uintptr
	r, _, _ := procSendMessageTimeout.Call(h, wmNull, 0, 0, smtoAbortIfHung,
		uintptr(hungTimeout.Milliseconds()), uintptr(unsafe.Pointer(&result)))
	return r != 0
}

// sameProgram reports whether process pid runs this executable.
func sameProgram(pid uint32) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &n); err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(windows.UTF16ToString(buf[:n])), filepath.Clean(self))
}

func endProcess(pid uint32) bool {
	p, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(p)
	if err := windows.TerminateProcess(p, 1); err != nil {
		return false
	}
	// Its single instance lock is released when the process is gone.
	ev, _ := windows.WaitForSingleObject(p, 5000)
	return ev == windows.WAIT_OBJECT_0
}
