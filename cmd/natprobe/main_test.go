package main

import (
	"reflect"
	"testing"
)

func TestParseTimeouts(t *testing.T) {
	got, err := parseTimeouts("15, 30,60,90,120")
	if err != nil || !reflect.DeepEqual(got, []int{15, 30, 60, 90, 120}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := parseTimeouts(""); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty list must mean none, got %v, %v", got, err)
	}
	for _, bad := range []string{"abc", "15,-1", "256", "15,,30"} {
		if _, err := parseTimeouts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
