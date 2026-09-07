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
	pushKinds := fs.String("push-kinds", envOr("LODESTAR_PUSH_KINDS", "sos,geofence"), "comma-separated envelope kinds that trigger push")
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

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(st, push.NewMulti(senders...), *adminToken, strings.Split(*pushKinds, ",")).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
