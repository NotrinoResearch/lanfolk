//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// appUserModelID matches the Start Menu shortcut the installer creates, so
// the taskbar groups the running window with the pinned shortcut.
const appUserModelID = "NotrinoResearch.Lanfolk"

var procSetAUMID = windows.NewLazySystemDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID")

func setAppUserModelID() {
	if procSetAUMID.Find() != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(appUserModelID)
	if err == nil {
		procSetAUMID.Call(uintptr(unsafePointer(p)))
	}
}

// systemUsesDarkTheme reports whether Windows apps use the dark theme. It is
// only used to pick the window's first background colour, before the page
// has loaded, so the window does not flash white in dark mode.
func systemUsesDarkTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}
