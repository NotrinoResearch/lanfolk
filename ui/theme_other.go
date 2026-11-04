//go:build !windows

package main

import "context"

func systemUsesDarkTheme() bool { return false }

func showWindow(context.Context) {}

func setAppUserModelID() {}

// windowVisible: without the Windows checks the window counts as visible.
func windowVisible() bool { return true }
