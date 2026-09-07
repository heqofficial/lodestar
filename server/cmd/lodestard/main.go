// lodestard is the Lodestar server: a single static binary that stores
// encrypted family-location envelopes, relays them over WebSocket, and
// optionally forwards alerts to ntfy / APNs.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/heqofficial/lodestar/server/internal/api"
	"github.com/heqofficial/lodestar/server/internal/push"
	"github.com/heqofficial/lodestar/server/internal/store"
)

func main() {
	slog.SetDefault(slog.New(newHandler()))

	fs := flag.NewFlagSet("lodestard", flag.ExitOnError)
	addr := fs.String("addr", envOr("LODESTAR_ADDR", ":8443"), "listen address")
	dsn := fs.String("data", envOr("LODESTAR_DSN", "lodestar.db"), "SQLite database path")
	ntfyURL := fs.String("ntfy-url", envOr("LODESTAR_NTFY_URL", ""), "ntfy base URL to relay alerts (empty = disabled)")
	ntfyToken := fs.String("ntfy-token", envOr("LODESTAR_NTFY_TOKEN", ""), "ntfy access token (optional)")
	adminToken := fs.String("admin-token", envOr("LODESTAR_ADMIN_TOKEN", ""), "token required for /admin (empty = open)")
	pushKinds := fs.String("push-kinds", envOr("LODESTAR_PUSH_KINDS", "sos,geofence,crash"), "comma-separated envelope kinds that trigger push")
	retentionDays := fs.Int("retention-days", intEnvOr("LODESTAR_RETENTION_DAYS", 90), "prune location history older than N days (0 = keep forever; sos/crash are always kept)")
	apnsKey := fs.String("apns-key", envOr("LODESTAR_APNS_KEY_PATH", ""), "APNs .p8 key path (enables iOS push)")
	apnsTeam := fs.String("apns-team", envOr("LODESTAR_APNS_TEAM_ID", ""), "Apple team ID")
	apnsKeyID := fs.String("apns-key-id", envOr("LODESTAR_APNS_KEY_ID", ""), "APNs key ID")
	apnsTopic := fs.String("apns-topic", envOr("LODESTAR_APNS_TOPIC", ""), "app bundle id")
	apnsEnv := fs.String("apns-env", envOr("LODESTAR_APNS_ENV", "development"), "development or production")
	_ = fs.Parse(os.Args[1:])

	st, err := store.Open(*dsn)
	if err != nil {
		slog.Error("store open", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	var senders []push.Sender
	if *ntfyURL != "" {
		senders = append(senders, push.NewNtfy(*ntfyURL, *ntfyToken))
		slog.Info("ntfy push enabled", "url", *ntfyURL)
	}
	if *apnsKey != "" {
		a, err := push.NewAPNs(push.APNsConfig{
			KeyPath: *apnsKey,
			TeamID:  *apnsTeam,
			KeyID:   *apnsKeyID,
			Topic:   *apnsTopic,
			Env:     *apnsEnv,
		})
		if err != nil {
			slog.Error("apns init", "err", err)
			os.Exit(1)
		}
		senders = append(senders, a)
		slog.Info("apns push enabled", "env", *apnsEnv)
	}

	if *retentionDays > 0 {
		runPrune(st, *retentionDays)
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				runPrune(st, *retentionDays)
			}
		}()
	}

	if err := st.Ping(context.Background()); err != nil {
		slog.Error("store ping", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(st, push.NewMulti(senders...), *adminToken, strings.Split(*pushKinds, ",")).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutdown", "err", err)
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

// runPrune removes envelopes older than the retention window (sos/crash
// alerts are exempt, kept forever by the store) and expired invite codes.
func runPrune(st *store.Store, days int) {
	n, err := st.PruneEnvelopes(time.Now().AddDate(0, 0, -days).UnixMilli())
	if err != nil {
		slog.Warn("prune", "err", err)
		return
	}
	if n > 0 {
		slog.Info("pruned envelopes", "n", n, "days", days)
	}
	if nInvites, err := st.PruneInvites(nowMS()); err != nil {
		slog.Warn("prune invites", "err", err)
	} else if nInvites > 0 {
		slog.Info("pruned invites", "n", nInvites)
	}
}

func nowMS() int64 { return time.Now().UnixMilli() }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnvOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
