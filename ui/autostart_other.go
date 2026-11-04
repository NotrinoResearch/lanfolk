//go:build !windows

package main

import "errors"

func startWithWindows() (bool, error) { return false, nil }

func setStartWithWindows(bool, string) error {
	return errors.New("starting at sign-in is only available on Windows")
}
