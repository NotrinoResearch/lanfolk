//go:build !automation || !windows

package main

// Release builds have no automation API (see automation_on.go).

func devBindings() []any { return nil }

func startAutomation(*wailsHost, *tray) {}

func automationAddress() string { return "" }
