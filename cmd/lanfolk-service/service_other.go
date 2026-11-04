//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"golang.zx2c4.com/wireguard/tun"
)

type tunDevice = tun.Device

func createTUN(name string, mtu int) (tun.Device, error) {
	return tun.CreateTUN(name, mtu)
}

func platformDataDir() string { return "/var/lib/lanfolk" }

func isService() bool { return false }

func runService() error { return errors.New("not a Windows service") }

func runForeground(o options) error {
	log, err := newLogger(o, true)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, o, log)
}

var errWindowsOnly = errors.New("service management is only available on Windows; use `run`")

func install(options) error { return errWindowsOnly }
func uninstall() error      { return errWindowsOnly }
func startService() error   { return errWindowsOnly }
func stopService() error    { return errWindowsOnly }
