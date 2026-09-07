// lodestard is the Lodestar server: a single static binary that stores
// encrypted family-location envelopes, relays them over WebSocket, and
// optionally forwards alerts to ntfy / APNs.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/heqofficial/lodestar/server/internal/run"
)

func main() {
	slog.SetDefault(slog.New(newHandler()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run.Server(ctx, os.Args[1:]); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// newHandler selects structured logging: JSON when LODESTAR_LOG_JSON=1,
// human-readable text otherwise.
func newHandler() slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("LODESTAR_LOG_JSON") == "1" {
		return slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.NewTextHandler(os.Stderr, opts)
}
