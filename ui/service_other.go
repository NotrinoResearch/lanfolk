//go:build !windows

package main

import "errors"

func startService() error { return errors.New("not supported") }

func closeSettingsWindow() {}
