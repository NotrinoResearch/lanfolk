//go:build !windows

package clock

import "time"

var base = time.Now()

// Nanotime returns a monotonic reading in nanoseconds.
func Nanotime() int64 { return int64(time.Since(base)) }
