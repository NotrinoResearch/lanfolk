//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

func unsafePointer(p *uint16) unsafe.Pointer { return unsafe.Pointer(p) }

func syscallN(p uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(p, args...)
	return r
}
