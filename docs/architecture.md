# Architecture

Lodestar is two pieces: a **Go server** (relay + ciphertext store) and a **Flutter app** (all intelligence lives here). The server is deliberately stupid: it stores opaque encrypted envelopes, fans them out over WebSocket, and never sees plaintext.

```
┌─────────────┐   HTTPS/WS   ┌──────────────────────┐   push    ┌──────────────┐
│ Flutter app │ ───────────► │  lodestard (Go)      │ ────────► │ ntfy (opt.)  │
│ iOS/Android │ ◄─────────── │  - identity & circles│           └──────────────┘
└─────────────┘   envelopes  │  - envelope relay    │
                             │  - SQLite (ciphertext│
                             │    + metadata)       │
                             └──────────────────────┘
```

## The envelope protocol

Everything a member shares — a location fix, a chat message, a check-in, an SOS, a geofence event — is one thing on the wire: an **envelope**.

```json
{
  "id": "01J...",
  "kind": "location | message | checkin | sos | geofence | circlekey",
  "circle_id": "…",
  "device_id": "…",
  "ts": 1736000000000,
  "nonce": "base64url (12 bytes)",
  "ciphertext": "base64url (ChaCha20-Poly1305 sealed)"
}
```

- `ciphertext` is sealed with the circle key (ChaCha20-Poly1305, random 12-byte nonce per envelope).
- The **server stores `nonce` + `ciphertext` verbatim** and indexes only metadata (kind, ts, device, circle). It cannot open envelopes.
- The app opens envelopes with the circle key and validates the sender's Ed25519 signature (signature inside the plaintext envelope).

## End-to-end encryption

- Each device generates an **Ed25519 identity keypair** (signing) and an **X25519 keypair** (encryption). Private halves live in the OS keystore/keychain (`flutter_secure_storage`).
- Each circle has a random 32-byte **circle key**.
- The circle creator distributes the circle key by sealing it to each member's X25519 public key (ECDH → HKDF-SHA256 → ChaCha20-Poly1305). Distribution messages travel as `circlekey` envelopes addressed to one member; the server routes them but can't read them.
- When a new member joins, the owner re-distributes the key (the app prompts: "grant access to new member").
- Envelopes are signed by the sender so members can verify who said what.

## Geofencing (on-device)

Places live in the app, not the server. Each device compares its own fixes against the circle's places and emits `geofence` envelopes (enter/leave) only on transitions, throttled per place. The server relays them like any envelope — it never learns place coordinates. (Places *are* shared circle-wide, but only as encrypted envelopes of kind `place`.)

## Live updates & push

- **WebSocket** (`/api/v1/ws`) — the app keeps a socket open to the server; envelopes fan out to circle members in real time. On Android the tracking foreground service keeps this connection alive.
- **ntfy (optional)** — the server can relay SOS/check-in/geofence events to a self-hosted ntfy topic per circle (payload is the encrypted envelope — safe to relay anywhere). Members can install the ntfy app and subscribe for push while Lodestar isn't running. iOS in-app APNs is roadmap.
- **No Firebase, no Google Play Services dependency** for messaging.

## Battery strategy

The app adapts its sampling to movement:

1. **Stationary** — GPS off; last known fix; wakes on a long timer to re-check.
2. **Walking** — ~30–60 s interval, low accuracy, distance filter 10 m.
3. **Driving** — ~10–15 s interval, high accuracy, distance filter 25 m.
4. **Geofence proximity** — when near a place boundary, bump to high accuracy briefly.

State machine lives in `apps/lodestar/lib/core/tracking/adaptive_tracker.dart`. Target: <5% battery/day. Android runs a foreground service (visible notification — honest about tracking); iOS uses the standard background location permission flow.

## Modules

### server/ (Go)
```
cmd/lodestard/       entrypoint, config
internal/api/        HTTP + WebSocket handlers, admin page
internal/store/      SQLite (pure-Go modernc driver), schema, queries
internal/push/       ntfy + APNs providers (APNs behind config)
internal/relay/      WebSocket hub (fan-out per circle)
```

### apps/lodestar/ (Flutter)
```
lib/core/api/        REST client + WebSocket client
lib/core/crypto/     identity keys, circle keys, envelope seal/open
lib/core/tracking/   adaptive tracker, geofence engine
lib/core/store/      local cache (sqflite), settings
lib/state/           ChangeNotifier stores (auth, circles, map, chat)
lib/screens/         onboarding, circles, map, places, history, chat, sos, settings
```

## Data model (server, SQLite)

| Table | Notes |
|---|---|
| devices | id, name, ed25519_pub, x25519_pub, token_hash, created_at |
| circles | id, name, color, owner_device_id, invite_code, created_at |
| circle_members | circle_id, device_id, role, avatar_color, sharing_enabled, joined_at |
| places | id, circle_id, name, lat, lng, radius_m, created_by |
| envelopes | id, circle_id, device_id, kind, ts, nonce, ciphertext |
| key_blobs | circle_id, device_id, ciphertext (addressed circlekey) |
| invites | code, circle_id, created_by, expires_at |

SQLite by default (zero-config self-hosting); the store layer is behind an interface so Postgres can be added later without protocol changes.

## Deployment

See [deployment.md](deployment.md). Docker Compose is the primary path; the server binary is CGO-free and runs anywhere Linux/ARM does.