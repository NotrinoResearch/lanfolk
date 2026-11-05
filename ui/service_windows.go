//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

const serviceName = "Lanfolk"

// startService starts the Lanfolk service when it is stopped, for example
// after the user turned Lanfolk off. The service lets signed-in users start
// it (D-039); with an older install, or when an administrator disabled it,
// this fails and the window keeps saying that the service is not running.
func startService() error {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(s)
	err = windows.StartService(s, 0, nil)
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}

var procPostMessage = user32.NewProc("PostMessageW")

// closeSettingsWindow asks an open Settings window (its own process) to close.
func closeSettingsWindow() {
	const wmClose = 0x0010
	cls, _ := windows.UTF16PtrFromString(windowClassFor(true))
	if h, _, _ := procFindWindow.Call(uintptr(unsafePointer(cls)), 0); h != 0 {
		procPostMessage.Call(h, wmClose, 0, 0)
	}
}
