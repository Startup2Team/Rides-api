#!/usr/bin/env bash
# First-boot provisioning for the Vultr (Johannesburg) box. Idempotent.
# Run as root on a fresh Ubuntu 24.04:
#   ssh root@<vm-ip> 'bash -s' < api-server/deploy/provision.sh
set -euo pipefail

echo "==> 1/6 System update"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y && apt-get upgrade -y
apt-get install -y curl git ufw ca-certificates

echo "==> 2/6 Docker + Compose plugin"
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
docker --version
docker compose version

echo "==> 3/6 Swap (2G) — keeps the 4GB box from OOMing during 'next build'"
if [ ! -f /swapfile ]; then
  fallocate -l 2G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
free -h | grep -i swap

echo "==> 4/6 Firewall — only SSH + HTTP(S)"
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable
ufw status verbose

echo "==> 5/6 App directory + deploy key"
mkdir -p /opt/rides
if [ ! -f /root/.ssh/id_ed25519 ]; then
  ssh-keygen -t ed25519 -N "" -f /root/.ssh/id_ed25519 -C "rides-vm-deploy"
fi

echo "==> 6/6 Done."
echo
echo "  Add this PUBLIC KEY as a read-only DEPLOY KEY on BOTH GitHub repos"
echo "  (repo Settings -> Deploy keys -> Add deploy key):"
echo "  ----------------------------------------------------------------"
cat /root/.ssh/id_ed25519.pub
echo "  ----------------------------------------------------------------"
echo
echo "  Then follow api-server/deploy/README.md (clone, certs, .env, compose up)."
