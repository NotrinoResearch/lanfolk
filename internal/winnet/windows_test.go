//go:build windows

package winnet

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

func TestNewAdapterRejectsForeignDevice(t *testing.T) {
	if _, err := NewAdapter(nil); err == nil {
		t.Error("nil device accepted")
	}
}

func TestPowershellExeExists(t *testing.T) {
	// The path must be absolute so that LocalSystem never searches PATH.
	if !isWindowsAbs(powershellExe()) {
		t.Errorf("powershell path not absolute: %s", powershellExe())
	}
}

func TestEnsureFirewallRulesUsesRunner(t *testing.T) {
	old := runner
	defer func() { runner = old }()
	var got []Command
	runner = func(_ context.Context, c Command) ([]byte, error) {
		got = append(got, c)
		return nil, nil
	}
	if err := EnsureFirewallRules(`C:\lanfolk.exe`); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFirewallRules(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("commands = %d", len(got))
	}
	if EnsureFirewallRules("relative.exe") == nil {
		t.Error("relative path accepted")
	}
}

// TestScriptsParse checks the syntax of the generated PowerShell scripts with
// the PowerShell parser. [scriptblock]::Create only parses; nothing runs and
// no firewall or network setting is touched.
func TestScriptsParse(t *testing.T) {
	ensure, err := ensureFirewallCommand(`C:\Program Files\Lanfolk\lanfolk-service.exe`)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Command{
		"ensure":  ensure,
		"remove":  removeFirewallCommand(),
		"private": setPrivateCommand(7),
	} {
		script := c.Args[len(c.Args)-1]
		parse := Command{
			Name: powershellExe(),
			Args: []string{"-NoProfile", "-NonInteractive", "-Command", "[void][scriptblock]::Create($env:LANFOLK_SCRIPT)"},
			Env:  []string{"LANFOLK_SCRIPT=" + script},
		}
		if _, err := runCommand(context.Background(), parse); err != nil {
			t.Errorf("%s script does not parse: %v", name, err)
		}
	}
}

func TestBroadcastCaptureNeedsAdmin(t *testing.T) {
	if elevated() {
		t.Skip("process is elevated; the not-elevated error cannot be tested")
	}
	_, err := StartBroadcastCapture(context.Background(), func([]byte) {})
	if !errors.Is(err, ErrNotElevated) {
		t.Fatalf("err = %v, want ErrNotElevated", err)
	}
}

func TestBroadcastCaptureStartStop(t *testing.T) {
	if !elevated() {
		t.Skip("needs an elevated process (raw socket)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := StartBroadcastCapture(ctx, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	select {
	case <-c.Done():
	default:
		t.Error("Done not closed after Close")
	}
}
