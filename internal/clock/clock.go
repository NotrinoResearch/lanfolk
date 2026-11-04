// Package clock provides a high-resolution monotonic clock for latency
// measurements. On Windows, time.Now advances in ~0.5 ms steps, which is too
// coarse for LAN round trips and the 1 ms tunnel overhead target, so
// QueryPerformanceCounter is used there.
package clock

import "time"

// Since returns the time elapsed since a Nanotime reading.
func Since(start int64) time.Duration { return time.Duration(Nanotime() - start) }
