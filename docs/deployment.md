# Deployment

Lodestar's server is a single CGO-free Go binary + SQLite. Any always-on machine with ~100 MB RAM free will do. Choose the option that matches your budget and comfort:

## Option A — Raspberry Pi at home ($0/mo)

Best for: maximum privacy, no recurring cost, you're comfortable with a Pi.

```bash
# on the Pi (64-bit OS)
git clone https://github.com/heqofficial/lodestar.git
cd lodestar && docker compose up -d --build
```

The image is built locally from source (there is no published registry image — building takes ~3 minutes the first time). Connect from anywhere with [Tailscale](https://tailscale.com) (free):

```bash
tailscale up
# Pi gets a stable address like 100.x.y.z — family phones reach
# http://100.x.y.z:8443 without exposing any ports to the internet
```

## Option B — Oracle Cloud free tier, step by step ($0/mo)

The **Always Free** tier gives you a permanent ARM VM (up to 4 OCPU / 24 GB RAM) — the Lodestar server uses ~2% of it. The console steps below are the only manual part (account + one VM); everything after is a single paste.

1. **Sign up** at <https://signup.oraclecloud.com> (email, then a credit card for identity verification — you are not charged while you stay on free resources).
2. In the console: **Compute → Instances → Create instance**.
3. Name it `lodestar`. Under **Image and shape**: image **Ubuntu 24.04**, then **Change shape** → select **`VM.Standard.A1.Flex`** (it is marked *Always Free eligible*) → leave 4 OCPU / 24 GB RAM.
4. Under **Networking**, keep the defaults; under **Add SSH keys** paste your public key (generate one: `ssh-keygen -t ed25519`).
5. **Create**, wait ~1 minute for **Running**, then copy the instance's **public IP** from the instance page.
6. **Open port 8443**: instance page → **VNIC → Security lists → default security list → Add Ingress Rules** → source `0.0.0.0/0`, destination port `8443` (TCP).
7. SSH in and run the one-shot setup (installs Docker, builds the server, protects `/admin`, opens ufw, verifies health):

```bash
ssh ubuntu@<public-ip>
bash <(curl -fsSL https://raw.githubusercontent.com/heqofficial/lodestar/main/deploy/oracle-setup.sh)
```

8. On each family phone: server URL `http://<public-ip>:8443`.

Caveats: the card is never charged while you stay on free shapes; Oracle may reclaim *idle* free VMs after 7 days (a running Lodestar server is not idle — this realistically never applies).

(Google Cloud's e2-micro free tier also works for a small circle: same `git clone && docker compose up -d --build` on a Debian image.)

## Option C — VPS (~$4/mo, best paid value)

**Hetzner CX22** (2 vCPU / 4 GB, ~€3.79/mo) or any comparable VPS. Use a fresh Debian/Ubuntu image, then:

```bash
apt update && apt install -y docker.io docker-compose-v2
git clone https://github.com/heqofficial/lodestar.git
cd lodestar && docker compose up -d --build
```

(Or skip Docker entirely — the server is a single static binary: `go build ./server/cmd/lodestard` and run it under systemd.)

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

The image is built from source, so upgrading is: pull the new code and rebuild.

```bash
git pull && docker compose up -d --build      # upgrade
sqlite3 data/lodestar.db ".backup backup.db"  # backup (ciphertext only — keys never leave phones)
```

Restoring the DB restores envelopes (history); members' keys survive independently on their phones, so a restore doesn't break circles.

## The web dashboard

`http://your-server:8443/admin` shows server status: uptime, counts, ntfy/APNs config state. It intentionally shows **no locations** — that data is encrypted and only the app can show it.

## Installing the app on phones (Android)

The release APK is built by CI on every push to `main`:

1. Open **Actions → `Android (release APK)`** on the GitHub repo → latest run → **Artifacts → `lodestar-apk`** → download (a zip of three per-ABI APKs).
2. Send the right one to each phone: `app-arm64-v8a-release.apk` for any phone from ~2018 onward (nearly all), `app-armeabi-v7a-release.apk` for old 32-bit phones, `app-x86_64-release.apk` for emulators. Each is ~19 MB instead of the 26 MB universal build.
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

## Verify your build

Before touching real phones, run the E2E suite — it builds the actual
binary and drives the full protocol over the network (register → circle →
join → envelopes → WebSocket relay → SOS push → kick → restart):

```bash
cd server && go test -v ./e2e/
```

## Family rollout checklist

1. [ ] Server up (any option above); test `curl http://host:8443/healthz` → `{"status":"ok"}` (this also probes the database — `503` means the store is wedged; Docker restarts it automatically via `HEALTHCHECK`)
2. [ ] Everyone installs the APK from CI artifacts (see above) — iOS via Xcode/TestFlight
3. [ ] First launch → enter server URL → create your circle
4. [ ] Share the invite code with family (they get their own keys on their own phones)
5. [ ] Add Places (Home, School, Work) — everyone gets enter/leave alerts
6. [ ] Start tracking on each phone; accept the battery-optimization dialog; test a drive
7. [ ] (Optional) Deploy ntfy and subscribe each phone's ntfy app to the circle topic for alerts when Lodestar isn't open (Android; iOS can use native APNs per the section above)