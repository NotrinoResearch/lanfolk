package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
)

func TestShutdownStopsOnceAfterTheReply(t *testing.T) {
	var stops, calls atomic.Int32
	stopped := make(chan struct{}, 4)
	h := withShutdown(func(context.Context, string, json.RawMessage) (any, error) {
		calls.Add(1)
		return "ok", nil
	}, func() { stops.Add(1); stopped <- struct{}{} }, slog.New(slog.DiscardHandler))

	if res, err := h(context.Background(), ipc.MethodStatus, nil); err != nil || res != "ok" || calls.Load() != 1 {
		t.Fatalf("status: %v %v, %d calls", res, err, calls.Load())
	}
	start := time.Now()
	if res, err := h(context.Background(), ipc.MethodShutdown, nil); err != nil || res != nil {
		t.Fatalf("shutdown: %v %v", res, err)
	}
	if stops.Load() != 0 {
		t.Fatal("stopped before the reply could be sent")
	}
	h(context.Background(), ipc.MethodShutdown, nil) // a second request changes nothing
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("never stopped")
	}
	if d := time.Since(start); d < shutdownDelay/2 {
		t.Fatalf("stopped after %v", d)
	}
	time.Sleep(2 * shutdownDelay)
	if n := stops.Load(); n != 1 {
		t.Fatalf("stopped %d times", n)
	}
	if calls.Load() != 1 {
		t.Fatal("shutdown reached the engine")
	}
}
