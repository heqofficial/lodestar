# Deployment

Lodestar's server is a single CGO-free Go binary + SQLite. Any always-on machine with ~100 MB RAM free will do. Choose the option that matches your budget and comfort:

## Option A — Raspberry Pi at home ($0/mo)

Best for: maximum privacy, no recurring cost, you're comfortable with a Pi.

```bash
# on the Pi (64-bit OS)
mkdir -p ~/lodestar && cd ~/lodestar
# copy the server binary (built for linux/arm64) or use Docker:
#   docker run -d --name lodestar -p 8443:8443 -v ~/lodestar/data:/data ghcr.io/heqofficial/lodestar:latest
```

Connect from anywhere with [Tailscale](https://tailscale.com) (free):

```bash
tailscale up
# Pi gets a stable address like 100.x.y.z — family phones reach
# https://100.x.y.z:8443 without exposing any ports to the internet
```

## Option B — Free cloud VM ($0/mo)

- **Oracle Cloud** free tier: ARM VM, 4 OCPU / 24 GB RAM, free forever (the sweet spot for a family server that also runs ntfy + Home Assistant).
- **Google Cloud** e2-micro free tier: enough for a small circle.

Same Docker command as above; open the firewall port only if you're not using Tailscale.

## Option C — VPS (~$4/mo, best paid value)

**Hetzner CX22** (2 vCPU / 4 GB, ~€3.79/mo) or any comparable VPS. Use a fresh Debian/Ubuntu image, then:

```bash
apt update && apt install -y docker.io docker-compose-v2
git clone https://github.com/heqofficial/lodestar.git
cd lodestar && docker compose up -d
```

Add a free Caddy reverse proxy for automatic HTTPS if you're exposing it publicly:

```bash
docker run -d -p 80:80 -p 443:443 \
  -v caddy_data:/data -v caddy_config:/config \
  caddy caddy reverse-proxy --from lodestar.example.com --to lodestar:8443
```

## Configuration

All configuration is via env vars (or flags, see `./lodestard -h`):

| Env | Default | Meaning |
|---|---|---|
| `LODESTAR_ADDR` | `:8443` | Listen address |
| `LODESTAR_DSN` | `lodestar.db` | SQLite file path |
| `LODESTAR_NTFY_URL` | (empty) | Self-hosted ntfy base URL to relay alerts to (e.g. `http://ntfy:80`) |
| `LODESTAR_NTFY_TOKEN` | (empty) | ntfy access token, if required |
| `LODESTAR_ADMIN_TOKEN` | (empty) | If set, `/admin` requires `?token=` |
| `LODESTAR_PUSH_KINDS` | `sos,geofence,crash` | Envelope kinds that trigger a push relay |
| `LODESTAR_RETENTION_DAYS` | `90` | Prune location history older than N days (`0` = keep forever). `sos`/`crash` alerts are always kept |
| `LODESTAR_LOG_JSON` | (unset) | `1` for JSON structured logs (easy log-shipping); default is human-readable text |
| `LODESTAR_APNS_KEY_PATH` | (empty) | APNs .p8 key path (enables iOS push) |
| `LODESTAR_APNS_TEAM_ID` | (empty) | Apple team ID |
| `LODESTAR_APNS_KEY_ID` | (empty) | APNs key ID |
| `LODESTAR_APNS_TOPIC` | (empty) | App bundle id (e.g. `dev.lodestar.app`) |
| `LODESTAR_APNS_ENV` | `development` | `development` or `production` |

## Data retention

By default the server prunes location history older than **90 days** (hourly, on the envelope's own timestamp). Emergency alerts (`sos`, `crash`) are never pruned. To keep everything forever, set `LODESTAR_RETENTION_DAYS=0` — storage grows ~15–20 MB/year per actively-driving device (a handful of KiB per envelope).

The server also enforces hard bounds so a single device can't bloat the DB: per-kind envelope throttles (30/min for location, 6/min for anything else), a 64 KiB envelope cap, and exact-duplicate rejection (retries are idempotent, replay doesn't duplicate rows). Phones prune their own envelope cache the same way (90 days / 20k per circle).

## Upgrades & backups

```bash
docker compose pull && docker compose up -d   # upgrade
sqlite3 data/lodestar.db ".backup backup.db"  # backup (ciphertext only — keys never leave phones)
```

Restoring the DB restores envelopes (history); members' keys survive independently on their phones, so a restore doesn't break circles.

## The web dashboard

`http://your-server:8443/admin` shows server status: uptime, counts, ntfy/APNs config state. It intentionally shows **no locations** — that data is encrypted and only the app can show it.

## Installing the app on phones (Android)

The release APK is built by CI on every push to `main`:

1. Open **Actions → `Android (release APK)`** on the GitHub repo → latest run → **Artifacts → `lodestar-apk`** → download.
2. Unzip `app-release.apk` and send it to the family (email, shared drive, or a link).
3. On each phone: open the APK → allow **"install unknown apps"** for your file manager / browser when prompted → install.
4. First launch: allow **Notifications** (alerts) and, when starting background tracking, **Location** (Allow all the time) and **"Allow battery optimization exemption"** (the system dialog Lodestar shows — this is what keeps tracking alive on modern phones).

The APK is signed with the debug key, which is fine for family sideloading. Production signing (Play Store / F-Droid) is future work.

### iOS push (APNs, optional)

Android gets alerts through the ntfy app (step 7 below). iOS can receive them natively through Apple Push Notification service:

1. You need an **Apple Developer account** ($99/yr). In the Apple Developer portal, create an **APNs Auth Key** (Keys → push notifications) and note the **Team ID**, **Key ID**, and the downloaded `.p8` file.
2. On the server, set `LODESTAR_APNS_KEY_PATH`, `LODESTAR_APNS_TEAM_ID`, `LODESTAR_APNS_KEY_ID`, `LODESTAR_APNS_TOPIC` (your app's bundle id) and `LODESTAR_APNS_ENV=production`.
3. In Xcode: enable the **Push Notifications** capability for the Runner target (creates `Runner.entitlements` with `aps-environment`) and build/install with your signing team.
4. The app requests notification permission at launch and registers its token automatically (server endpoint `PUT /api/v1/devices/push`). Tokens Apple reports dead are dropped automatically.

The push is a wake-up signal with a title (e.g. "🚨 SOS from Alice"); the encrypted content itself is fetched from the server when the app opens — ciphertext never touches Apple's network.

## Family rollout checklist

1. [ ] Server up (any option above); test `curl http://host:8443/healthz` → `{"status":"ok"}` (this also probes the database — `503` means the store is wedged; Docker restarts it automatically via `HEALTHCHECK`)
2. [ ] Everyone installs the APK from CI artifacts (see above) — iOS via Xcode/TestFlight
3. [ ] First launch → enter server URL → create your circle
4. [ ] Share the invite code with family (they get their own keys on their own phones)
5. [ ] Add Places (Home, School, Work) — everyone gets enter/leave alerts
6. [ ] Start tracking on each phone; accept the battery-optimization dialog; test a drive
7. [ ] (Optional) Deploy ntfy and subscribe each phone's ntfy app to the circle topic for alerts when Lodestar isn't open (Android; iOS can use native APNs per the section above)