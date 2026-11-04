package main

import (
	"net"
	"os"
	"os/exec"
	"strconv"
)

// The Settings window is the same executable started with --settings. Wails
// v2 has one window per process, so a second process gives Settings its own
// resizable window and taskbar entry; both talk to the same service, which
// keeps them in sync through its events.

// spec is the size and behaviour of one of the two windows.
type spec struct {
	Title               string
	Width, Height       int
	MinWidth, MinHeight int
	HideOnClose         bool // the main window closes to the tray
	InstanceSuffix      string
}

func windowSpec(settings bool) spec {
	if settings {
		return spec{Title: "Lanfolk Settings", Width: 780, Height: 560, MinWidth: 640, MinHeight: 460, InstanceSuffix: ".settings"}
	}
	return spec{Title: appTitle, Width: 380, Height: 640, MinWidth: 340, MinHeight: 520, HideOnClose: true}
}

// settingsArgs are the command-line arguments of the Settings process.
func settingsArgs(pipe, automation string) []string {
	args := []string{"--settings", "-pipe", pipe}
	if automation != "" {
		if host, port, err := net.SplitHostPort(automation); err == nil {
			if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65535 {
				args = append(args, "-automation", net.JoinHostPort(host, strconv.Itoa(p+1)))
			}
		}
	}
	return args
}

// Mode tells the page which window it is in: "main" or "settings".
func (a *App) Mode() string {
	if a.mode == "" {
		return "main"
	}
	return a.mode
}

// OpenSettings opens the Settings window, or brings it to the front when it
// is already open (its single-instance lock handles the second start).
func (a *App) OpenSettings() error {
	exe, err := os.Executable()
	if err != nil {
		return localError("Could not find Lanfolk.exe", err)
	}
	cmd := exec.Command(exe, settingsArgs(a.addr, a.automation)...)
	if err := cmd.Start(); err != nil {
		return localError("Could not open Settings", err)
	}
	go cmd.Wait()
	return nil
}
