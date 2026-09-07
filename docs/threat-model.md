# Threat model

Lodestar moves the most sensitive data a family has: everyone's location, continuously. This document states exactly what we protect, what we don't, and what an attacker of each type can and cannot do. **If you find a way to break an assumption below, that's a security bug — report it (see SECURITY.md).**

## Actors

| Actor | Description |
|---|---|
| **Circle member** | Legitimate family member with the app and keys |
| **Server operator** | Runs `lodestard` — could be you (self-hosted), or a stranger if you use someone else's instance |
| **Network observer** | Can read/capture traffic between app and server |
| **Data broker / ad tech** | Wants to collect and resell location data |
| **Malicious member** | A circle member with legitimate keys who misbehaves |
| **Device thief / forensic examiner** | Gets physical access to a family member's phone |

## Guarantees

### 1. The server never learns locations, places, or messages (core guarantee)

- All circle content travels as envelopes sealed with the circle key (ChaCha20-Poly1305, random nonces).
- The circle key is only ever stored (a) on member devices in the OS keystore, or (b) inside envelopes sealed to member public keys.
- The server's database contains ciphertext plus metadata: `(circle, device, kind, ts)`. Metadata alone reveals *that* a member moved at some cadence, but not *where*.
- Geofences are evaluated on-device. Place coordinates exist only inside encrypted envelopes.
- **Server operator cannot** read anyone's location, even with full DB + memory access.

### Known metadata leak (accepted)
The server sees: who is a member of which circle, when each device posts envelopes, envelope kinds, and message sizes. An operator with network access also sees IP addresses (mitigate: Tailscale/VPN). This metadata is inherent to a relay design and is documented, not hidden. No analytics or logging beyond operational basics is permitted to leave the server.

## 2. Authentication & identity

- Each device authenticates with a random 256-bit bearer token; the server stores only `SHA-256(token)`.
- Device identity for envelopes is an Ed25519 keypair — a server cannot forge a member's envelopes (it would need their private key).
- A **stolen bearer token** lets an attacker read envelope *metadata* and post envelopes as that member; it does NOT let them read ciphertext.
- A **stolen phone** (unlocked) grants full access to that member's circle content — same as stealing someone's unlocked phone with Life360 installed. The OS keystore protects keys when locked.
- **Malicious member** can read everything in the circle (they have the key — they're family) and could post forged envelopes *as themselves*. Sender signatures prevent forging *as others*.

## 3. Transport

- Production deployments MUST use TLS (Caddy/nginx) or a private tunnel (Tailscale/WireGuard). Without it, a network observer learns everything a server learns (metadata) and could tamper with relayed ciphertext (fail-closed: app rejects envelopes that fail signature verification).
- Self-signed certs are fine for private LANs; the app trusts the pinned fingerprint you configure.

## 4. Data minimization

- No phone number, no email, no real names required. Display names are chosen by members.
- No analytics, no crash reporting, no advertising SDKs, no third-party network calls. (The only outbound app traffic: your server, and OSM tile servers for the map background — map tiles reveal only the map areas you *view*, not your location; tile fetches are unauthenticated and cacheable.)
- ntfy relay (optional): payloads relayed are encrypted envelopes; the ntfy server cannot read them.

## 5. Non-goals (accepted risks, documented)

| Risk | Why accepted | Mitigation |
|---|---|---|
| Crash detection & 911 dispatch | Requires sensor fusion, carrier SMS integration, and liability | Roadmap: circle alerting first |
| iOS background push | APNs requires Apple Developer account | Roadmap; Android covered by foreground service; ntfy app subscription works on iOS today |
| Third-party audit | No funding yet | Reproducible builds + open code; audits welcome |
| Malicious member screenshots your map | You trusted them with the key | Family context: you chose your circle |
| Server-side spam/DoS | Self-hosted scale | Basic rate limiting per device; deploy behind your own reverse proxy |
| Legal subpoena for ciphertext | Server can't decrypt anyway | Keep keys only on devices; offer key backup phrases members control |

## Cryptographic inventory

| Primitive | Use | Library |
|---|---|---|
| Ed25519 | Device identity, envelope signatures | `cryptography` (pure Dart) |
| X25519 | Circle-key distribution ECDH | `cryptography` (pure Dart) |
| HKDF-SHA256 | ECDH → shared sealing key | `cryptography` |
| ChaCha20-Poly1305 | Envelope sealing (AEAD) | `cryptography` |
| SHA-256 | Token hashing at rest | stdlib (Go) |

## Reporting

Any break of the "server never learns locations" guarantee, signature forgery, or key extraction without device unlock is a **critical** issue. See SECURITY.md for reporting.