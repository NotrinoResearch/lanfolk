//go:build windows

package main

import (
	"testing"
	"unsafe"
)

// The layout must match NOTIFYICONDATAW exactly: with a wrong size or
// offsets Shell_NotifyIcon still succeeds but no notification appears
// (found in the 0.3.0 release).
func TestNotifyIconDataLayout(t *testing.T) {
	var n notifyIconData
	type field struct {
		name      string
		got, want uintptr
	}
	is64 := unsafe.Sizeof(uintptr(0)) == 8
	want := map[bool][]uintptr{
		// size, szInfo, uTimeout, szInfoTitle, dwInfoFlags, guidItem, hBalloonIcon
		true:  {976, 304, 816, 820, 948, 952, 968},
		false: {956, 288, 800, 804, 932, 936, 952},
	}[is64]
	for _, f := range []field{
		{"cbSize", unsafe.Sizeof(n), want[0]},
		{"szInfo", unsafe.Offsetof(n.Info), want[1]},
		{"uTimeout", unsafe.Offsetof(n.TimeoutOrVersion), want[2]},
		{"szInfoTitle", unsafe.Offsetof(n.InfoTitle), want[3]},
		{"dwInfoFlags", unsafe.Offsetof(n.InfoFlags), want[4]},
		{"guidItem", unsafe.Offsetof(n.GuidItem), want[5]},
		{"hBalloonIcon", unsafe.Offsetof(n.BalloonIcon), want[6]},
	} {
		if f.got != f.want {
			t.Errorf("%s at %d, want %d", f.name, f.got, f.want)
		}
	}
}

func TestPutUTF16Truncates(t *testing.T) {
	var buf [8]uint16
	putUTF16(buf[:], "a very long title that does not fit")
	if buf[7] != 0 {
		t.Fatal("not zero-terminated")
	}
	var ok [16]uint16
	putUTF16(ok[:], "Hi")
	if ok[0] != 'H' || ok[1] != 'i' || ok[2] != 0 {
		t.Fatalf("%v", ok[:3])
	}
}
