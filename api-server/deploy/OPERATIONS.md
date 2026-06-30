# Rides — Operations Guide (how the live system works)

Server: **Vultr Johannesburg**, IP **139.84.251.242**, Ubuntu 24.04, 8 GB / 4 vCPU / 150 GB.

---

## 1. Access the server (SSH)

From your Mac (your SSH key is already authorized):
```bash
ssh root@139.84.251.242
```
- Login is **key-based** (your `~/.ssh/id_ed25519`). No password needed.
- A root password also exists (Vultr generated it) as a backup, but prefer the key.
- Recommended hardening later: disable password login (`PasswordAuthentication no` in `/etc/ssh/sshd_config`).

Everything below is run **on the box** after you SSH in.

---

## 2. The architecture (big picture)

```
  Phone / Browser
        │  HTTPS
        ▼
  Cloudflare  (DNS, CDN, DDoS, edge TLS)
        │  HTTPS (Origin cert, Full-strict)
        ▼
  ┌──────────────── Vultr box (139.84.251.242) ───────────────┐
  │  nginx :80/:443  ── reverse proxy + TLS                    │
  │     ├── api.rides.rw    → api    :8080  (Go)               │
  │     ├── admin.rides.rw  → admin  :3000  (Next.js)          │
  │     └── rides.rw        → admin  :3000  (landing)          │
  │                                                            │
  │  api ──► postgres :5432  (PostGIS, source of truth)        │
  │  api ──► redis    :6379  (matching, sessions, geo index)   │
  └────────────────────────────────────────────────────────────┘
        │                         │
        ▼                         ▼
  Cloudflare R2            Africa's Talking (OTP SMS)
  (driver docs)            Firebase / Google Maps / MoMo (pending)
```

5 containers, all from `docker-compose.prod.yml`. DB & Redis have **no public ports** — only nginx is exposed (80/443).

---

## 3. Where everything lives on the box

```
/opt/rides/
├── Rides-api/                 # backend repo (pushed from your Mac)
│   └── api-server/            # ← run all commands from here
│       ├── docker-compose.prod.yml   # the whole stack definition
│       ├── .env                       # ALL secrets & config (gitignored)
│       ├── Dockerfile                 # builds the Go API image
│       ├── migrations/                # 100 .sql files = your DB schema
│       └── nginx/
│           ├── nginx.conf             # reverse-proxy routing
│           └── certs/                 # rides.rw.pem + rides.rw.key (TLS)
└── Rides-web/                 # admin + landing repo (Next.js)
    └── Dockerfile
```

---

## 4. Docker — see and control everything

Always `cd /opt/rides/Rides-api/api-server` first (compose reads files there).

```bash
C="docker compose -f docker-compose.prod.yml"   # handy alias for this session

$C ps                      # list the 5 containers + status
docker images              # images on disk
$C logs -f api             # live API logs (Ctrl-C to stop)
$C logs --tail 50 admin    # last 50 admin lines
$C restart api             # restart one service
$C up -d --build api       # rebuild + restart after code change
$C down                    # stop everything (keeps data volumes)
$C up -d                   # start everything
docker stats               # live CPU/RAM per container
```

The five services: **postgres** (DB), **redis** (cache/geo), **api** (Go backend), **admin** (Next.js admin+landing), **nginx** (TLS + routing).

---

## 5. Database (PostgreSQL + PostGIS)

Connect to a live SQL shell:
```bash
docker exec -it api-server-postgres-1 psql -U rides_prod -d rideplatform
```
Inside psql:
```
\dt                 -- list all 53 tables
\d driver_profiles  -- show one table's columns/indexes
\d+ rides           -- detailed
SELECT count(*) FROM customer_profiles;
\q                  -- quit
```
- The schema is **defined by the files in `migrations/`** — they run automatically on API boot. To change the schema you add a new numbered migration; never edit applied ones.
- **Backup** (do regularly):
  ```bash
  docker exec api-server-postgres-1 pg_dump -U rides_prod rideplatform | gzip > ~/backup-$(date +%F).sql.gz
  ```
  (Vultr auto-backups also snapshot the whole disk nightly.)

---

## 6. Redis

It's password-protected (password is `REDIS_PASSWORD` in `.env`):
```bash
docker exec -it api-server-redis-1 redis-cli -a "$(grep '^REDIS_PASSWORD=' .env | cut -d= -f2)"
```
Then: `PING`, `KEYS *`, `INFO memory`, `DBSIZE`. Used for: driver geo-index (matching), sessions, OTP throttling, idempotency keys.

---

## 7. How code gets deployed (NOT a GitHub pull!)

The box does **not** clone from GitHub. Code is shipped **from your Mac** with `git archive` over SSH — a snapshot of a branch, piped straight onto the box. To redeploy after you push changes:

**Backend (Rides-api):**
```bash
# on your Mac, in the Rides-api repo:
git archive --format=tar deploy/vultr-jnb | ssh root@139.84.251.242 \
  'tar -x -C /opt/rides/Rides-api --overwrite'
ssh root@139.84.251.242 'cd /opt/rides/Rides-api/api-server && \
  docker compose -f docker-compose.prod.yml up -d --build api'
```

**Web (Rides-web, from the `main`-based branch):**
```bash
# on your Mac, in the Rides-web repo:
git archive --format=tar deploy/docker-main | ssh root@139.84.251.242 \
  'rm -rf /opt/rides/Rides-web && mkdir -p /opt/rides/Rides-web && tar -x -C /opt/rides/Rides-web'
ssh root@139.84.251.242 'cd /opt/rides/Rides-api/api-server && \
  docker compose -f docker-compose.prod.yml up -d --build admin'
```
> The `.env` and `nginx/certs/` live only on the box and are never overwritten by these pushes.
> (Later we can switch to GitHub deploy keys + `git pull` if you prefer pull-based deploys.)

---

## 8. The `.env` — all configuration

Location: `/opt/rides/Rides-api/api-server/.env` (permissions 600, gitignored).
To change a value:
```bash
nano .env                 # edit
docker compose -f docker-compose.prod.yml up -d api   # restart api to apply
```
What's in it (grouped): core (ENV, ports, ADMIN_ORIGIN), Postgres + Redis passwords, JWT secrets, **AT_*** (OTP SMS), FIREBASE/GOOGLE_MAPS (pending), **MOMO_*** (pending), **STORAGE_*** (R2, live), and the `DEV_*` safety gates (kept `false`).

---

## 9. nginx — the reverse proxy

Config: `/opt/rides/Rides-api/api-server/nginx/nginx.conf` (mounted into the nginx container).
Routing: `api.rides.rw→api:8080`, `admin.rides.rw→admin:3000` (root 302s to `/admin`), `rides.rw/www→admin:3000` (landing). TLS = Cloudflare Origin cert in `nginx/certs/`.
To change routing:
```bash
nano nginx/nginx.conf
docker exec api-server-nginx-1 nginx -t        # test config
docker exec api-server-nginx-1 nginx -s reload # apply with zero downtime
```

---

## 10. OTP (one-time passwords)

- **Wired** via Africa's Talking (live account `travels-rides`).
- Flow: app sends phone number → API generates a code → sends it as an SMS via AT → user types it back → API verifies (codes & throttling tracked in Redis).
- Needs: AT wallet balance (top up — ~RWF 113 now) and, ideally, an approved Alphanumeric Sender ID.
- Test it by registering/logging in from the mobile app with a real number.

---

## 11. MoMo (payments) — status: NOT live yet

- `PAYMENTS_ENABLED=false`, `MOMO_ENVIRONMENT=sandbox`. Only the subscription key is set.
- To go live: provision a MoMo **API User + API Key** (sandbox first), wire/verify RequestToPay (collections), complete MTN **Go-Live** (KYC with your RDB docs), then set production keys + `PAYMENTS_ENABLED=true`.
- The public webhook is already secured: `https://api.rides.rw/api/v1/webhooks/momo/callback` (guarded by `MOMO_WEBHOOK_SECRET`).

---

## 12. Connect the mobile app

Point the app at the live API instead of ngrok/localhost. In the **Rides-mobile** repo's env:
```
EXPO_PUBLIC_API_BASE_URL=https://api.rides.rw/api/v1
```
Rebuild/restart the app. Live tracking (WebSockets) also flows through `api.rides.rw` — nginx is configured to upgrade WebSocket connections.

---

## 13. Quick health checks

```bash
curl https://api.rides.rw/health                 # {"data":{"status":"ok"}}
curl -I https://admin.rides.rw/admin/login       # 200
docker compose -f docker-compose.prod.yml ps     # all "Up"/"healthy"
df -h / ; free -h                                # disk + memory
```
