// lodestard is the Lodestar server: a single static binary that stores
// encrypted family-location envelopes, relays them over WebSocket, and
// optionally forwards alerts to ntfy / APNs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
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
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	var senders []push.Sender
	if *ntfyURL != "" {
		senders = append(senders, push.NewNtfy(*ntfyURL, *ntfyToken))
		log.Printf("ntfy push enabled -> %s", *ntfyURL)
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
			log.Fatalf("apns: %v", err)
		}
		senders = append(senders, a)
		log.Printf("apns push enabled (%s)", *apnsEnv)
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

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(st, push.NewMulti(senders...), *adminToken, strings.Split(*pushKinds, ",")).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("lodestard listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	fmt.Println("bye")
}

// runPrune removes envelopes older than the retention window; sos/crash
// alerts are exempt (kept forever by the store).
func runPrune(st *store.Store, days int) {
	n, err := st.PruneEnvelopes(time.Now().AddDate(0, 0, -days).UnixMilli())
	if err != nil {
		log.Printf("prune: %v", err)
		return
	}
	if n > 0 {
		log.Printf("pruned %d envelopes older than %d days", n, days)
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
