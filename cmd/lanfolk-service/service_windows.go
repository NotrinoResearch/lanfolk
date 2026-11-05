//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/store"
	"github.com/NotrinoResearch/lanfolk/internal/winnet"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"golang.zx2c4.com/wireguard/tun"
)

const serviceName = "Lanfolk"

type tunDevice = tun.Device

// adapterGUID keeps the Wintun adapter identity stable across restarts so
// Windows keeps its network profile.
var adapterGUID = &windows.GUID{Data1: 0x4c616e66, Data2: 0x6f6c, Data3: 0x6b31, Data4: [8]byte{0x8e, 0x2f, 0x5b, 0x31, 0x77, 0x00, 0x0a, 0x4d}}

func createTUN(name string, mtu int) (tun.Device, error) {
	tun.WintunTunnelType = "Lanfolk"
	return tun.CreateTUNWithRequestedGUID(name, adapterGUID, mtu)
}

func platformDataDir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "Lanfolk")
	}
	return `C:\ProgramData\Lanfolk`
}

func isService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

func raisePriority() {
	windows.SetPriorityClass(windows.CurrentProcess(), windows.HIGH_PRIORITY_CLASS)
}

func runForeground(o options) error {
	log, err := newLogger(o, true)
	if err != nil {
		return err
	}
	raisePriority()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		c := make(chan os.Signal, 1)
		signalNotify(c)
		<-c
		cancel()
	}()
	return serve(ctx, o, log)
}

type handler struct{ o options }

func (h handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	log, err := newLogger(h.o, false)
	if err != nil {
		return true, 1
	}
	raisePriority()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, h.o, log) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				log.Error("service stopped with an error", "err", err)
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func runService() error {
	return svc.Run(serviceName, handler{o: options{dataDir: defaultDataDir(), pipe: `\\.\pipe\lanfolk`}})
}

func install(o options) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return errors.New("the Lanfolk service is already installed")
	}
	s, err = m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:  "Lanfolk",
		Description:  "Lanfolk virtual LAN: connects your private gaming networks.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	if err := allowUsersToStart(s); err != nil {
		fmt.Fprintln(os.Stderr, "warning: the app cannot start the service after it was turned off:", err)
	}
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 24*60*60)
	if err := winnet.EnsureFirewallRules(exe); err != nil {
		return fmt.Errorf("firewall rules: %w", err)
	}
	// The data folder is private to the service before anything is written.
	// The service repeats this at every start, so a failure here only warns.
	if _, err := prepareDataDir(o); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not make the data folder private:", err)
	}
	// The installing user owns the control pipe.
	if sid := currentUserSID(); sid != "" {
		st, err := store.Open(o.dataDir)
		if err != nil {
			return err
		}
		if err := st.Update(func(d *store.Data) error { d.OwnerSID = sid; return nil }); err != nil {
			return err
		}
	}
	fmt.Println("Lanfolk service installed.")
	return nil
}

// serviceSDDL is Windows' default service DACL with one addition: signed-in
// (interactive) users may start the service (RP), so the app can turn
// Lanfolk on again after the user turned it off. Stopping (WP) and changing
// it stay with administrators and LocalSystem; the owner stops it through the
// control pipe (service.shutdown).
const serviceSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

func allowUsersToStart(s *mgr.Service) error {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func currentUserSID() string {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run as administrator): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		stopAndWait(s)
		if err := s.Delete(); err != nil {
			s.Close()
			return fmt.Errorf("delete service: %w", err)
		}
		s.Close()
	}
	if err := winnet.RemoveFirewallRules(); err != nil {
		return fmt.Errorf("remove firewall rules: %w", err)
	}
	fmt.Println("Lanfolk service removed. Your identity and friends stay in", defaultDataDir())
	return nil
}

func stopAndWait(s *mgr.Service) {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return
	}
	deadline := time.Now().Add(20 * time.Second)
	for st.State != svc.Stopped && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return
		}
	}
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the Lanfolk service is not installed: %w", err)
	}
	defer s.Close()
	return s.Start()
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the Lanfolk service is not installed: %w", err)
	}
	defer s.Close()
	stopAndWait(s)
	return nil
}
