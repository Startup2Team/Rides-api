# Deploy runbook — Vultr Johannesburg (single box)

Brings up `api.rides.rw` + `admin.rides.rw` on one VM, behind Cloudflare.
Five services (Postgres+PostGIS, Redis, Go API, Next.js admin, Nginx) run via
`docker-compose.prod.yml`. Run all compose commands from
`/opt/rides/Rides-api/api-server`.

## 0. Prerequisites
- Vultr **Cloud Compute, Johannesburg, Ubuntu 24.04, 4 GB** instance (SSH key added).
- Domain `rides.rw` on **Cloudflare** (nameservers delegated).

## 1. Provision the box
```bash
ssh root@<VM_IP> 'bash -s' < api-server/deploy/provision.sh
```
Copy the printed **deploy public key** → add it as a read-only **Deploy key** on
**both** GitHub repos (`Rides-api` and `Rides-web`).

## 2. Clone both repos side by side
```bash
ssh root@<VM_IP>
cd /opt/rides
git clone -b main          git@github.com:Startup2Team/Rides-api.git
git clone -b deploy/docker git@github.com:Startup2Team/Rides-web.git
cd Rides-api/api-server
```
> Use the branches you want live (e.g. the admin redesign branch for Rides-web).

## 3. Cloudflare DNS + TLS
**DNS** (Cloudflare → DNS): A records, **Proxied (orange)**:
| Type | Name  | Content    |
|------|-------|------------|
| A    | api   | `<VM_IP>`  |
| A    | admin | `<VM_IP>`  |

**SSL/TLS mode**: set to **Full (strict)**.

**Origin cert** (Cloudflare → SSL/TLS → Origin Server → Create Certificate,
hostnames `rides.rw, *.rides.rw`). Paste the two files on the box:
```bash
mkdir -p /opt/rides/Rides-api/api-server/nginx/certs
nano nginx/certs/rides.rw.pem   # paste the certificate
nano nginx/certs/rides.rw.key   # paste the private key
chmod 600 nginx/certs/rides.rw.key
```

## 4. Production env + secrets
```bash
cd /opt/rides/Rides-api/api-server
cp .env.production.example .env
# generate secrets:
echo "JWT_ACCESS_SECRET=$(openssl rand -hex 64)"
echo "JWT_REFRESH_SECRET=$(openssl rand -hex 64)"
echo "MOMO_WEBHOOK_SECRET=$(openssl rand -hex 32)"
nano .env   # paste secrets; set DB/Redis passwords; add R2 / AT / MoMo / Maps keys
```
Drop the Firebase service-account JSON next to the compose file:
```bash
nano /opt/rides/Rides-api/api-server/firebase-service-account.json
```

## 5. Launch
```bash
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs -f api   # migrations run on boot
```

## 6. Verify
- `https://api.rides.rw/health` → `{"status":"ok"}`
- `https://admin.rides.rw/admin/login` → admin login loads
- Register the MoMo callback with MTN:
  `https://api.rides.rw/api/v1/webhooks/momo/callback`

## Updating later
```bash
cd /opt/rides/Rides-api && git pull
cd /opt/rides/Rides-web && git pull
cd /opt/rides/Rides-api/api-server && docker compose -f docker-compose.prod.yml up -d --build
```

## Backups (do this from day one)
Single box = Postgres and Redis share the disk. Enable **Vultr auto-backups** at
the instance level, and/or a nightly `pg_dump` piped to Cloudflare R2.

## Hardening follow-ups (post-testing)
- Restrict the origin firewall to Cloudflare IP ranges (so the box is only
  reachable via Cloudflare, not by its raw IP).
- Move Postgres → managed PostGIS and Redis → managed when load grows
  (see `../DEPLOYMENT.md` scaling path).
