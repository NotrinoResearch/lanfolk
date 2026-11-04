//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// The per-user Run key starts the app at sign-in. The installer writes the
// same value when "Start Lanfolk when I sign in" is checked.
var runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

const runValueName = "Lanfolk"

// startWithWindows reports whether the app starts at sign-in.
func startWithWindows() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// setStartWithWindows adds or removes the Run value for exe. The app starts
// hidden in the tray when Windows starts it.
func setStartWithWindows(on bool, exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValueName, `"`+exe+`" --minimized`)
	}
	if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
