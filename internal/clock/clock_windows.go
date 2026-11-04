//go:build windows

package clock

import (
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")
	freq     int64
)

func init() {
	procQPF.Call(uintptr(unsafe.Pointer(&freq)))
	if freq <= 0 {
		freq = 10_000_000
	}
}

// Nanotime returns a monotonic reading in nanoseconds.
func Nanotime() int64 {
	var c int64
	procQPC.Call(uintptr(unsafe.Pointer(&c)))
	// Split to avoid overflow for long uptimes.
	sec := c / freq
	rem := c % freq
	return sec*1_000_000_000 + rem*1_000_000_000/freq
}
