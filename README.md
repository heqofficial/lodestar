# Lodestar ⭐

![CI](https://github.com/heqofficial/lodestar/actions/workflows/ci.yml/badge.svg)

**Your family's guiding star.** A free, open-source, end-to-end-encrypted family location app — Life360's usefulness without the surveillance business model.

- 🔒 **Private by architecture** — locations are end-to-end encrypted. Even your own server only stores ciphertext and can never see where anyone is.
- 🏠 **Self-hosted** — runs on a Raspberry Pi, an old laptop, or a $4/month VPS. Zero subscriptions, zero ads, zero data selling.
- 📱 **Cross-platform** — Android + iOS, one codebase (Flutter), with a Go server.
- 🔋 **Battery-friendly** — adaptive, motion-aware tracking designed to use under ~5% battery per day.
- 🧑‍🤝‍🧑 **Consent-first** — every member controls what they share, when, and with whom. Pausing is visible to the circle, because trust beats surveillance.
- 🌍 **Open** — AGPL-3.0, reproducible builds, self-hostable by anyone.

## Why not Life360?

Life360 has been caught selling precise family location data to a dozen data brokers (The Markup, 2021), paywalls its best features (history, driving reports, crash detection) behind Gold/Platinum subscriptions, drains batteries, and keeps expanding the permissions it asks for. People keep asking for a free, private alternative — and the existing open-source options are either developer tools (OwnTracks), fleet software (Traccar), or abandoned prototypes (GroupTrack). Lodestar exists to close that gap.

## Feature set

- Circles (invite by code) with member avatars and colors
- Live family map (OpenStreetMap, no paid API keys)
- Adaptive background tracking with motion-aware intervals
- Places + on-device geofence enter/leave events
- End-to-end encrypted location envelopes, chat, check-ins, and SOS
- Location history timeline (decrypted on-device only)
- Circle chat (E2E encrypted)
- One-tap check-ins and SOS broadcast
- **SOS you can't lose** — an unsent SOS is persisted to disk and delivered on next launch, even if the app is killed while offline.
- **Driving reports** — encrypted end-of-drive summaries: distance, duration, top/avg speed, speeding episodes (≥5 s over your threshold), hard braking (≥2.5 m/s²). Free forever — Life360 paywalls this.
- **Crash alerting** — on-device impact detection (accelerometer jolt or GPS speed-drop) confirmed by the vehicle stopping, then an encrypted crash alert to the circle (pushes even if sharing is paused — it's an emergency).
- Per-member sharing controls (pause, battery mode, speeding threshold)
- **Sign out / revoke this device** — one tap wipes local keys and revokes the server token instantly.
- Self-hosted push via [ntfy](https://ntfy.sh) (optional; crash + SOS alert by default)
- Minimal web dashboard (server status, no locations)

**Hardened server:** panic recovery (one bad request can't kill the server), per-device + per-kind rate limiting, exact-duplicate/replay rejection, key-blob integrity checks, immediate socket revocation on member removal, 90-day retention pruning, constant-time admin auth, versioned `/healthz`. See [docs/threat-model.md](docs/threat-model.md).

**Roadmap:** Home Assistant add-on, wearables, multi-language, public instance, pet circles.

## Repository layout

```
server/   Go server — single static binary, SQLite, WebSocket relay,
          plus the minimal /admin dashboard (embedded, no locations)
apps/     Flutter app (iOS + Android)
docs/     Architecture, threat model, deployment guide
```

## Quick start

### 1. Run the server

The easiest path is Docker:

```bash
docker compose up -d
```

Or run the binary directly (no CGO, single file):

```bash
cd server
go build -o lodestard ./cmd/lodestard
./lodestard -addr :8443 -data data.db
```

The server listens on `http://localhost:8443` (set `LODESTAR_ADDR`/`-addr` for your LAN or VPS address). See [docs/deployment.md](docs/deployment.md) for Raspberry Pi, Tailscale, and VPS setups.

### 2. Build the app

```bash
cd apps/lodestar
flutter pub get
flutter run          # point it at your server on first launch
```

### 3. Create your family circle

In the app: **Create circle** → share the invite code with family → they join → everyone appears on the map.

> 💡 Family members install the app once, join the circle with a code, and are tracked with their explicit consent. Anyone can pause sharing at any time, and the circle can see that they paused — transparency is the point.

## Security model

| Property | Design |
|---|---|
| Location confidentiality | End-to-end encrypted (X25519 + ChaCha20-Poly1305); server stores ciphertext only |
| Authentication | Per-device bearer tokens; no passwords stored server-side |
| Identity | Ed25519 device keys; circle key distributed to members' public keys |
| Transport | TLS everywhere in production (Tailscale makes this free) |
| Data minimization | No phone number, optional email, zero analytics, zero trackers |
| Geofencing | Evaluated on-device; the server never sees plaintext places or positions |
| Encryption at rest | Keys in OS keychain/keystore; optional passphrase backup |

Read the full [threat model](docs/threat-model.md) and [architecture](docs/architecture.md).

## Costs — the honest numbers

| Item | Cost |
|---|---|
| Server (Raspberry Pi at home) | $0/mo (electricity aside) |
| Server (Oracle free ARM VM or Google e2-micro) | $0/mo |
| Server (Hetzner CX22 VPS — best paid value) | ~$4/mo |
| Android distribution (F-Droid) | $0 (metadata + fastlane lane in `fastlane/`; first submission is a manual F-Droid repo PR) |
| iOS distribution (TestFlight/App Store) | $99/yr Apple Developer (unavoidable for normal iOS installs) |
| Push notifications (self-hosted ntfy) | $0 |

## Development

- `server/` — Go 1.25+, `go vet ./... && go test -race ./...` (fuzz: `go test -fuzz=Fuzz -fuzztime=10s ./internal/api`)
- `apps/lodestar/` — Flutter 3.47+ (Dart 3.9), `flutter analyze && flutter test`
- CI runs both on every push (GitHub Actions)

See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/architecture.md](docs/architecture.md).

## License

AGPL-3.0 — free forever, and anyone who runs a Lodestar server for others must share their changes too. See [LICENSE](LICENSE).