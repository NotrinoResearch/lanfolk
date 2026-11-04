//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// useTestRunKey points the autostart code at a scratch key for one test.
func useTestRunKey(t *testing.T) {
	t.Helper()
	old := runKeyPath
	runKeyPath = `Software\Lanfolk-UI-Test\Run`
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, runKeyPath)
		registry.DeleteKey(registry.CURRENT_USER, `Software\Lanfolk-UI-Test`)
		runKeyPath = old
	})
}

func TestAutostartRoundTrip(t *testing.T) {
	useTestRunKey(t)
	exe := `C:\Program Files\Lanfolk\Lanfolk.exe`
	if on, err := startWithWindows(); err != nil || on {
		t.Fatalf("fresh key: on=%v err=%v", on, err)
	}
	if err := setStartWithWindows(true, exe); err != nil {
		t.Fatal(err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := k.GetStringValue(runValueName)
	k.Close()
	if err != nil || v != `"C:\Program Files\Lanfolk\Lanfolk.exe" --minimized` {
		t.Fatalf("Run value = %q, %v", v, err)
	}
	if on, err := startWithWindows(); err != nil || !on {
		t.Fatalf("after enable: on=%v err=%v", on, err)
	}
	if err := setStartWithWindows(false, exe); err != nil {
		t.Fatal(err)
	}
	if on, _ := startWithWindows(); on {
		t.Fatal("still enabled after disable")
	}
	// Disabling twice is not an error.
	if err := setStartWithWindows(false, exe); err != nil {
		t.Fatal(err)
	}
}
