package clock

import (
	"testing"
	"time"
)

func TestMonotonicAndFine(t *testing.T) {
	a := Nanotime()
	time.Sleep(5 * time.Millisecond)
	b := Nanotime()
	if d := time.Duration(b - a); d < 4*time.Millisecond || d > 200*time.Millisecond {
		t.Fatalf("elapsed %v", d)
	}
	// Resolution must be well below the 0.5 ms step of time.Now on Windows.
	min := int64(1 << 62)
	for i := 0; i < 1000; i++ {
		x := Nanotime()
		y := Nanotime()
		for y == x {
			y = Nanotime()
		}
		if y-x < min {
			min = y - x
		}
	}
	if min > int64(50*time.Microsecond) {
		t.Fatalf("clock step %v is too coarse", time.Duration(min))
	}
}
