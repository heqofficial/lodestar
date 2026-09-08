// Package run wires the lodestard server together: flags, store, push
// providers, HTTP server, and the retention janitor.
//
// It lives outside main so the e2e suite can boot the exact production
// wiring in-process on machines that refuse to execute freshly built
// binaries (e.g. Windows Smart App Control), while CI still exercises the
// real binary.
package run

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/heqofficial/lodestar/server/internal/api"
	"github.com/heqofficial/lodestar/server/internal/push"
	"github.com/heqofficial/lodestar/server/internal/store"
)

// Server runs the server until ctx is cancelled, then shuts down
// gracefully. It returns nil on a clean shutdown, or the fatal error
// (flag/store/listen failure) that should terminate the process.
func Server(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("lodestard", flag.ContinueOnError)
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
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := store.Open(*dsn)
	if err != nil {
		return fmt.Errorf("store open: %w", err)
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
			return fmt.Errorf("apns init: %w", err)
		}
		senders = append(senders, a)
		slog.Info("apns push enabled", "env", *apnsEnv)
	}

	if *retentionDays > 0 {
		runPrune(st, *retentionDays)
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runPrune(st, *retentionDays)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("store ping: %w", err)
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: api.New(st, push.NewMulti(senders...), *adminToken, strings.Split(*pushKinds, ",")).Handler(),
		// ReadTimeout bounds the whole request phase (headers + body): a
		// hostile client that trickles a request body would otherwise hold
		// a connection (and goroutine) open indefinitely. WebSocket
		// connections are unaffected — the upgrade happens well within the
		// window, and after the hijack the socket manages its own deadlines.
		ReadTimeout:       60 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("shutdown", "err", err)
	}
	return nil
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
	if nInvites, err := st.PruneInvites(time.Now().UnixMilli()); err != nil {
		slog.Warn("prune invites", "err", err)
	} else if nInvites > 0 {
		slog.Info("pruned invites", "n", nInvites)
	}
	// Abandoned registrations (each app reinstall creates a fresh device
	// row) have no memberships and can never be used again — drop them
	// after 6 months so the devices table stays bounded.
	if nDevices, err := st.PruneDevices(time.Now().AddDate(0, 0, -180).UnixMilli()); err != nil {
		slog.Warn("prune devices", "err", err)
	} else if nDevices > 0 {
		slog.Info("pruned devices", "n", nDevices)
	}
}

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
