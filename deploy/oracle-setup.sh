#!/usr/bin/env bash
# Oracle Cloud Always-Free provisioning for the Lodestar server.
#
# Run ONCE on a fresh Ubuntu 24.04 VM, as the ubuntu user (sudo available):
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/heqofficial/lodestar/main/deploy/oracle-setup.sh)
#
# What it does:
#   1. installs Docker + the compose plugin
#   2. opens port 8443/tcp (ufw; the OCI security list must also allow it — see docs/deployment.md)
#   3. clones the repo to /opt/lodestar
#   4. generates a random admin token into /opt/lodestar/.env (protects /admin)
#   5. builds and starts the server with `docker compose up -d --build`
#   6. waits for /healthz and prints the connection details
#
# Safe to re-run: every step is idempotent.
set -euo pipefail

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

log "Installing Docker + compose plugin"
sudo apt-get update -qq
sudo apt-get install -y -qq docker.io docker-compose-v2 ufw curl >/dev/null
sudo systemctl enable --now docker

log "Opening port 8443/tcp in ufw (SSH stays on 22)"
sudo ufw allow 22/tcp >/dev/null 2>&1 || true
sudo ufw allow 8443/tcp >/dev/null 2>&1 || true
sudo ufw --force enable >/dev/null

log "Cloning the Lodestar repository"
if [ ! -d /opt/lodestar/.git ]; then
  sudo git clone --depth 1 https://github.com/heqofficial/lodestar.git /opt/lodestar
else
  sudo git -C /opt/lodestar pull --ff-only
fi
cd /opt/lodestar

# Protect /admin with a random token unless the admin already chose one.
if [ ! -f .env ]; then
  TOKEN=$(head -c 24 /dev/urandom | base64 | tr -d '=+/' | cut -c1-24)
  echo "LODESTAR_ADMIN_TOKEN=$TOKEN" | sudo tee .env >/dev/null
  echo "  admin token: $TOKEN  (also saved in /opt/lodestar/.env)"
else
  echo "  keeping existing /opt/lodestar/.env"
fi

log "Building and starting the server (first build takes a few minutes)"
sudo docker compose up -d --build

log "Waiting for the server to become healthy"
for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8443/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
  [ "$i" = 60 ] && { echo "ERROR: server did not become healthy" >&2; exit 1; }
done

log "Done. Your server is live."
echo "  Public IP : $(curl -fsS --max-time 5 ifconfig.me 2>/dev/null || echo 'check the OCI console')"
echo "  Health    : http://127.0.0.1:8443/healthz"
echo
echo "  On your phones, use:  http://<this VM's public IP>:8443"
echo "  Admin dashboard:      http://<this VM's public IP>:8443/admin?token=<from .env>"
echo
echo "  Updates later:        sudo -i; cd /opt/lodestar && git pull && docker compose up -d --build"
echo "  Logs:                 sudo docker compose -f /opt/lodestar/docker-compose.yml logs -f"