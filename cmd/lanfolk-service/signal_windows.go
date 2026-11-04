//go:build windows

package main

import (
	"os"
	"os/signal"
)

func signalNotify(c chan os.Signal) { signal.Notify(c, os.Interrupt) }
