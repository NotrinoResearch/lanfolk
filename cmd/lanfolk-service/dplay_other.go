//go:build !windows

package main

import (
	"context"
	"log/slog"

	"github.com/NotrinoResearch/lanfolk/internal/engine"
)

// startDirectPlayGuard does nothing outside Windows (D-038).
func startDirectPlayGuard(context.Context, *engine.Engine, string, *slog.Logger) func() {
	return func() {}
}
