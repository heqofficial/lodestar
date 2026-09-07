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
| `LODESTAR_APNS_KEY_PATH` | (empty) | APNs .p8 key path (enables iOS push — roadmap) |
| `LODESTAR_APNS_TEAM_ID` | (empty) | Apple team ID |
| `LODESTAR_APNS_KEY_ID` | (empty) | APNs key ID |
| `LODESTAR_APNS_TOPIC` | (empty) | App bundle id (e.g. `dev.lodestar.app`) |
| `LODESTAR_APNS_ENV` | `development` | `development` or `production` |

## Data retention

By default the server prunes location history older than **90 days** (hourly, on the envelope's own timestamp). Emergency alerts (`sos`, `crash`) are never pruned. To keep everything forever, set `LODESTAR_RETENTION_DAYS=0` — storage grows ~15–20 MB/year per actively-driving device (a handful of KiB per envelope).

The server also enforces hard bounds so a single device can't bloat the DB: per-kind envelope throttles (30/min for location, 6/min for anything else), a 64 KiB envelope cap, and exact-duplicate rejection (retries are idempotent, replay doesn't duplicate rows).

## Upgrades & backups

```bash
docker compose pull && docker compose up -d   # upgrade
sqlite3 data/lodestar.db ".backup backup.db"  # backup (ciphertext only — keys never leave phones)
```

Restoring the DB restores envelopes (history); members' keys survive independently on their phones, so a restore doesn't break circles.

## The web dashboard

`http://your-server:8443/admin` shows server status: uptime, counts, ntfy/APNs config state. It intentionally shows **no locations** — that data is encrypted and only the app can show it.

## Family rollout checklist

1. [ ] Server up (any option above); test `curl http://host:8443/healthz` → `{"status":"ok"}`
2. [ ] Everyone installs the app (Android: APK/F-Droid; iOS: TestFlight once a build exists)
3. [ ] First launch → enter server URL → create your circle
4. [ ] Share the invite code with family (they get their own keys on their own phones)
5. [ ] Add Places (Home, School, Work) — everyone gets enter/leave alerts
6. [ ] Set battery mode per device; test a drive
7. [ ] (Optional) Deploy ntfy and subscribe each phone's ntfy app to the circle topic for alerts when Lodestar isn't open