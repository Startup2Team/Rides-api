# Deploy Rides API on Fly.io (Johannesburg)

Target URL: **https://api.rides.rw**

This guide deploys the Go API plus managed Postgres (PostGIS) and Redis in Fly's `jnb` (Johannesburg) region — closest mature cloud to Rwanda.

## What runs on Fly.io

| Service | Fly product | Notes |
|---|---|---|
| Go API | Fly App `rides-api` | Docker image from `Dockerfile` |
| PostgreSQL + PostGIS | Fly Managed Postgres (MPG) | Enable PostGIS at create time |
| Redis | Fly Redis or Redis on a Machine | GEO index, sessions, rate limits |
| Object storage | **Not on Fly** | Use Cloudflare R2 or S3 (already in config) |

The admin dashboard (`Rides-web`) and marketing site (`rides.rw`) are separate deploys. Only the backend API lives here.

## Prerequisites

1. [Fly.io account](https://fly.io) (you have this)
2. [flyctl CLI](https://fly.io/docs/hands-on/install-flyctl/) installed
3. DNS access for `rides.rw` (registrar or Cloudflare)
4. Strong JWT secrets (generate with `openssl rand -hex 32`)

```bash
# Install flyctl (macOS)
brew install flyctl

# Log in
fly auth login
```

## Step 1 — Create Postgres (PostGIS)

```bash
fly mpg create \
  --name rides-db \
  --region jnb \
  --pg-major-version 16 \
  --plan starter \
  --volume-size 10 \
  --enable-postgis-support
```

Save the connection string Fly prints. It becomes `DATABASE_URL`.

Enable PostGIS on the database (if not auto-enabled):

```sql
CREATE EXTENSION IF NOT EXISTS postgis;
```

## Step 2 — Create Redis

Option A — Fly Redis (simplest):

```bash
fly redis create --name rides-redis --region jnb --no-replicas
```

Option B — Redis on a small Machine (matches local `appendonly yes`):

Deploy a dedicated Redis app or use a volume-backed machine. For MVP, Fly Redis is fine.

Note the `REDIS_URL` (e.g. `redis://default:password@fly-rides-redis.upstash.io` or internal Fly URL).

## Step 3 — Create and deploy the API app

From `api-server/`:

```bash
cd /Users/paccee/Pac/Rides-api/api-server

# First deploy (creates app from fly.toml)
fly launch --no-deploy --copy-config --name rides-api --region jnb

# Set secrets (required)
fly secrets set \
  DATABASE_URL='postgres://...' \
  REDIS_URL='redis://...' \
  JWT_ACCESS_SECRET='...' \
  JWT_REFRESH_SECRET='...' \
  ADMIN_ORIGIN='https://admin.rides.rw'

# Optional integrations (add when ready)
# fly secrets set AT_API_KEY='...' AT_USERNAME='...' AT_SENDER_ID='...'
# fly secrets set STORAGE_PROVIDER='r2' STORAGE_BUCKET='...' STORAGE_KEY_ID='...' STORAGE_SECRET='...'

fly deploy
```

Check logs:

```bash
fly logs
fly status
```

Default Fly URL (before custom domain):

```
https://rides-api.fly.dev/health
```

## Step 4 — Attach custom domain `api.rides.rw`

```bash
fly certs add api.rides.rw
```

Fly prints DNS records. Add them where `rides.rw` DNS is managed.

### If DNS is on Cloudflare

1. Add record (Fly will tell you which type):

   | Type | Name | Target |
   |---|---|---|
   | `CNAME` | `api` | `rides-api.fly.dev` |

2. **SSL/TLS mode:** Full (strict) is fine once Fly cert is issued.
3. **Proxy:** Orange cloud (proxied) is OK for API; WebSockets work through Cloudflare.

### If DNS is at the .rw registrar

Add the exact `A` / `AAAA` / `CNAME` records Fly shows for `api.rides.rw`.

Verify certificate:

```bash
fly certs show api.rides.rw
```

Wait until status is **Ready** (can take 5–30 minutes after DNS propagates).

## Step 5 — Test the API

```bash
# Health
curl https://api.rides.rw/health

# Public pricing
curl https://api.rides.rw/api/v1/pricing

# Swagger UI in browser
open https://api.rides.rw/swagger
```

### Mobile app base URL

```
https://api.rides.rw/api/v1
```

WebSocket endpoint:

```
wss://api.rides.rw/api/v1/ws
```

### Admin dashboard env

In `Rides-web`:

```env
NEXT_PUBLIC_API_URL=https://api.rides.rw/api/v1
```

Ensure `ADMIN_ORIGIN` on the API matches the admin URL (e.g. `https://admin.rides.rw`).

## Suggested domain map

| Subdomain | Purpose | Host |
|---|---|---|
| `rides.rw` | Marketing website | Vercel / static host |
| `api.rides.rw` | Backend API | **Fly.io `rides-api`** |
| `admin.rides.rw` | Admin dashboard | Vercel / Fly / Render |
| `status.rides.rw` | Uptime page | Better Uptime / Instatus |

## Important production notes

1. **Single API machine** — WebSocket hub is not multi-instance safe yet. Keep `min_machines_running = 1` in `fly.toml`.
2. **Migrations** — App runs migrations on startup. First deploy may take longer.
3. **Firebase** — `FIREBASE_SERVICE_ACCOUNT_PATH` expects a file. For Fly, either bake into image (not recommended) or add a startup script that writes the secret to `/app/firebase-service-account.json` from an env var.
4. **Backups** — Fly MPG includes automated Postgres backups. Snapshot Redis separately for disaster recovery.
5. **CORS** — Set `ADMIN_ORIGIN` to your real admin URL before testing the dashboard against production API.

## Useful commands

```bash
fly apps list
fly status -a rides-api
fly logs -a rides-api
fly ssh console -a rides-api
fly secrets list -a rides-api
fly scale count 1 -a rides-api   # keep at 1 until WS clustering is done
```

## Troubleshooting

| Problem | Fix |
|---|---|
| Cert stuck on "Awaiting configuration" | DNS record missing or wrong; run `dig api.rides.rw` |
| App crashes on start | `fly logs` — usually missing `DATABASE_URL` or Redis unreachable |
| PostGIS errors | Ensure MPG was created with `--enable-postgis-support` and 2GB+ RAM |
| 502 from Fly | Health check failing; confirm `/health` returns 200 |
| CORS errors from admin | Set `ADMIN_ORIGIN` secret and redeploy |
