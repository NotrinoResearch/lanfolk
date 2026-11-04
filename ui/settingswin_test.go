package main

import (
	"reflect"
	"testing"
)

func TestSettingsWindowArgs(t *testing.T) {
	got := settingsArgs(`\\.\pipe\lanfolk`, "")
	want := []string{"--settings", "-pipe", `\\.\pipe\lanfolk`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	// An automation build hands the settings window its own port.
	got = settingsArgs(`\\.\pipe\lanfolk-mock-1`, "127.0.0.1:9400")
	want = []string{"--settings", "-pipe", `\\.\pipe\lanfolk-mock-1`, "-automation", "127.0.0.1:9401"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	// A port that cannot be parsed is not passed on.
	got = settingsArgs("p", "localhost")
	if len(got) != 3 {
		t.Fatalf("args = %q", got)
	}
}

func TestModeIsReported(t *testing.T) {
	a := NewApp("x", &fakeHost{})
	if a.Mode() != "main" {
		t.Fatalf("default mode = %q", a.Mode())
	}
	a.mode = "settings"
	if a.Mode() != "settings" {
		t.Fatalf("mode = %q", a.Mode())
	}
}

func TestSettingsWindowOptions(t *testing.T) {
	m := windowSpec(false)
	s := windowSpec(true)
	if m.Width >= s.Width {
		t.Fatalf("the settings window must be wider than the main window: %+v %+v", m, s)
	}
	if !m.HideOnClose || s.HideOnClose {
		t.Fatal("only the main window hides to the tray on close")
	}
	if m.InstanceSuffix == s.InstanceSuffix {
		t.Fatal("the two windows need separate single-instance locks")
	}
}
