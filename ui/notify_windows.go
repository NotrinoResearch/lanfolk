//go:build windows

package main

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The tray icon belongs to fyne.io/systray, which has no notification
// API. Windows shows a notification for a tray icon when the icon is
// modified with NIF_INFO, so showNotice finds the icon's window (class
// "SystrayClass" in this process; fyne always uses icon ID 100) and sets
// the info text. Windows 10 and 11 show it as a toast under the app's
// AppUserModelID, honour Focus Assist (no toasts during a full-screen game)
// and keep it in the Notification Center.

const (
	systrayClass  = "SystrayClass"
	systrayIconID = 100

	nimModify            = 0x1
	nifInfo              = 0x10
	niifNoSound          = 0x10
	niifRespectQuietTime = 0x80
)

// notifyIconData is NOTIFYICONDATAW. uTimeout and uVersion are a union in
// C: one field here. (fyne's copy has two, which shifts every later field
// by 4 bytes; Windows then accepts the call and shows nothing.)
type notifyIconData struct {
	Size                       uint32
	Wnd                        windows.Handle
	ID, Flags, CallbackMessage uint32
	Icon                       windows.Handle
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	TimeoutOrVersion           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GuidItem                   windows.GUID
	BalloonIcon                windows.Handle
}

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellNotify  = shell32.NewProc("Shell_NotifyIconW")
	procFindWindowEx = user32.NewProc("FindWindowExW")
)

var errNoTrayIcon = errors.New("the tray icon is not ready")

// trayWindow returns this process's systray window.
func trayWindow() windows.Handle {
	cls, _ := windows.UTF16PtrFromString(systrayClass)
	self := uint32(os.Getpid())
	var h uintptr
	for {
		h, _, _ = procFindWindowEx.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		windows.GetWindowThreadProcessId(windows.HWND(h), &pid)
		if pid == self {
			return windows.Handle(h)
		}
	}
}

// putUTF16 copies s into dst, cut to fit with a terminating zero.
func putUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func showNotice(n notice) error {
	h := trayWindow()
	if h == 0 {
		return errNoTrayIcon
	}
	nid := notifyIconData{Wnd: h, ID: systrayIconID, Flags: nifInfo,
		InfoFlags: niifNoSound | niifRespectQuietTime}
	nid.Size = uint32(unsafe.Sizeof(nid))
	putUTF16(nid.InfoTitle[:], n.Title)
	putUTF16(nid.Info[:], n.Body)
	if r, _, err := procShellNotify.Call(nimModify, uintptr(unsafe.Pointer(&nid))); r == 0 {
		return err
	}
	return nil
}
