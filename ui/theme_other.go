//go:build !windows

package main

import "context"

func systemUsesDarkTheme() bool { return false }

func showWindow(context.Context) {}

func setAppUserModelID() {}
