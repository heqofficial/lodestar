# Security

Lodestar handles the most sensitive data a family has: where its members are, in real time. Security is the product. Please treat it that way.

## Reporting a vulnerability

**Do not open a public issue.** Email the maintainers privately, or — once the repository is public — use the GitHub private vulnerability reporting flow.

Include:
1. What you found (proof of concept or a clear description)
2. Impact (what an attacker could do)
3. Affected component (server, app, protocol, docs)

We aim to acknowledge reports within 48 hours and to ship fixes as fast as the severity warrants. Sensitive fixes get a coordinated disclosure window before any public announcement.

## Threat model

The design is documented in [docs/threat-model.md](docs/threat-model.md). In short, we assume:

- **The server operator is not trusted.** The whole protocol is designed so a server can never see plaintext locations, places, or messages.
- **The device owner is trusted** with their own keys (keystore/keychain protected).
- **Transport is not trusted** — TLS (or a WireGuard/Tailscale tunnel) is mandatory in production.

## Security properties we promise

- End-to-end encryption of all circle content (locations, chat, check-ins, SOS, geofence events) — server holds ciphertext only.
- No tracking/analytics SDKs, no third-party data processors, no ad frameworks.
- Device tokens are stored hashed (SHA-256) server-side.
- Geofences evaluate on-device; the server never learns place locations.

## What we do NOT promise (yet)

- Crash detection and emergency-call dispatch (roadmap).
- iOS push notification delivery in-app (APNs integration is roadmap; Android works via the always-on foreground service and optional ntfy).
- A third-party security audit (none has been performed — independent audits are welcome; see CONTRIBUTING).