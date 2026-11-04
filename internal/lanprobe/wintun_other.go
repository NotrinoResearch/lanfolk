//go:build !windows

package lanprobe

import (
	"context"
	"fmt"
	"io"
)

// SelfTestOptions controls RunWintunSelfTest.
type SelfTestOptions struct {
	NoFirewall bool
}

// RunWintunSelfTest is only available on Windows.
func RunWintunSelfTest(_ context.Context, w io.Writer, _ SelfTestOptions) int {
	fmt.Fprintln(w, "wintun-selftest is only available on Windows.")
	return 2
}
