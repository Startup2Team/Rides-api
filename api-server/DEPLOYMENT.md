# Rides — Backend Deployment Guide

Where to host the backend so it is **fast for Rwanda**, **trusted**, **cheap for
testing**, and **able to scale**. No fluff. Prices are USD/month, approximate —
verify on the provider's pricing page before committing.

---

## 1. What we actually run (and the hard constraints)

| Component | Detail | Constraint it forces |
|---|---|---|
| Go API (Docker) | chi, WebSockets (tracking, negotiation) | Must allow **long-lived WebSocket** connections + a **public HTTPS URL** |
| PostgreSQL | **PostGIS required** (geo matching) | Managed PG must support the **PostGIS extension** |
| Redis | GEO commands (driver index), sessions, idempotency | Needs **GEOADD/GEOSEARCH**; location pings are frequent (cost driver) |
| Object storage | driver KYC documents (S3-compatible) | Code already supports **S3 / Cloudflare R2 / MinIO** |
| MoMo webhook | MTN sends payment callbacks | Needs a **stable public HTTPS endpoint** |

**Rwanda latency reality:** there is no major cloud *inside* Rwanda. Lowest
latency to Kigali comes from **Johannesburg, South Africa (~30–60 ms)**, then
Europe (~150–180 ms). Pick the region, not just the provider.

---

## 2. Recommendation

### ✅ For testing now (best balance): one VPS in Johannesburg
Run the **exact `docker-compose` we already use** on a single Johannesburg VM.
Same setup as dev, lowest Rwanda latency, full control, cheapest path to a real
public URL + WebSockets + PostGIS + Redis + MinIO all in one place.

- **Provider: Vultr — Johannesburg** (reputable, real JNB location, ~40 ms to Kigali).
  - Start: **2 GB / 1 vCPU — $12/mo**. Comfortable: **4 GB / 2 vCPU — $24/mo**.
- Object storage: **Cloudflare R2** (free 10 GB, no egress fees) — or MinIO on the same box for testing.
- TLS + domain: **free** (Caddy/Traefik auto-Let's Encrypt) + a ~$10/year domain.

**Total to start: ~$12–24/mo, everything included.**

### 💸 Absolute cheapest (accept ~150 ms latency): Hetzner
- **Hetzner CX22 — 2 vCPU / 4 GB — ~$5/mo** (Falkenstein/Helsinki, Europe), 20 TB traffic.
- Same `docker-compose`. ~150–180 ms to Rwanda — **fine for testing**, not ideal for production real-time.
- **Total: ~$6/mo** (+ ~$1 backups).

> Pick Vultr-JNB if Rwanda latency matters for your testing (it does for live
> location/negotiation). Pick Hetzner if you only want the cheapest possible box.

---

## 3. Cost table

| Item | Testing (single VM) | Light production |
|---|---|---|
| API + PG + Redis + MinIO | Vultr JNB 2–4 GB — **$12–24** | split out (below) |
| Object storage | Cloudflare R2 — **$0** (≤10 GB) | R2 — ~$0–5 |
| Domain | ~$10/**year** | ~$10/year |
| TLS | $0 (Let's Encrypt) | $0 |
| Backups | Vultr auto-backup +20% (~$2–5) | included in managed PG |
| **Monthly total** | **~$15–30** | **~$45–70** |

**Light-production split (when one box isn't enough):**
- API: keep on Vultr JNB, or **Fly.io `jnb`** machines ($3–6 each, scale horizontally).
- Postgres+PostGIS: **Supabase Pro $25** (8 GB, PostGIS built in, daily backups) — easiest managed PostGIS.
- Redis: **Upstash** pay-as-you-go (note: location pings can exceed the free 10k/day fast — budget a few $).
- Storage: **Cloudflare R2** (~$0–5).

---

## 4. Deploy steps (single VM — the testing path)

1. **Create the VM** (Vultr → Deploy → Cloud Compute → **Johannesburg** → Ubuntu 24.04 → 2–4 GB). Add your SSH key.
2. **Install Docker + Compose**: `curl -fsSL https://get.docker.com | sh`.
3. **Clone the repo** and copy `docker-compose.yml` + a production `.env`.
4. **Domain + TLS**: point an A record (e.g. `api.rides.rw`) at the VM IP. Put **Caddy** in front of the API (auto-HTTPS in ~3 lines) — this gives the public HTTPS URL MoMo needs.
5. **Production `.env`** (the must-change values):
   - `ENV=production` (this turns OFF all dev shortcuts: geofence skip, 2FA skip, dev OTP, payment auto-confirm)
   - new strong `JWT_ACCESS_SECRET` / `JWT_REFRESH_SECRET` (`openssl rand -hex 64`)
   - `MOMO_API_KEY` / `MOMO_SUBSCRIPTION_KEY` / `MOMO_ENVIRONMENT`
   - `STORAGE_PROVIDER=r2` + bucket/keys/CDN (or keep MinIO for testing)
   - real `AT_*` (Africa's Talking SMS) so OTP actually sends
   - `FIREBASE_SERVICE_ACCOUNT_PATH` for push notifications
6. **MoMo webhook URL**: register `https://api.rides.rw/api/v1/webhooks/momo/callback` with MTN.
7. `docker compose up -d --build`. Migrations run on boot.
8. **Point the mobile app** `EXPO_PUBLIC_API_BASE_URL` at `https://api.rides.rw/api/v1` (replaces ngrok).

> ⚠️ A single box runs Postgres + Redis on the same disk. Enable **automated
> backups** (Vultr backups, or a nightly `pg_dump` to R2) from day one — this is
> the one real risk of the single-VM setup.

---

## 5. Scaling path (when testing → real load)

1. **Vertical first:** bump the VM (4 → 8 → 16 GB). Covers a lot.
2. **Move Postgres off the box** to managed PostGIS (Supabase Pro / Neon / Azure SA-North) — removes the biggest single-box risk and gives backups + PITR.
3. **Move Redis to Upstash** (or a managed Redis in JNB).
4. **Run 2+ API machines** behind a load balancer (Fly.io `jnb` makes this trivial; the API is stateless except Redis/PG, so it scales horizontally). WebSockets work across machines because presence lives in Redis.
5. **Storage** is already external (R2) — no change needed.

The app is already built for this: stateless API, Redis for hot state, PG as the
source of truth, object storage external. Nothing in the code blocks horizontal
scaling.

---

## 6. Trust / data residency notes

- **Vultr, Hetzner, Cloudflare, AWS, Azure, GCP, Supabase, Upstash, Fly.io** are all reputable.
- Closest *trusted-major-cloud* regions to Rwanda for production: **GCP `africa-south1` (Johannesburg)**, **Azure South Africa North**, **AWS `af-south-1` (Cape Town)**. Use these if you later need enterprise compliance/data-residency; they cost more and are more complex than the VPS path above.
- For **testing**, the single Johannesburg VPS is the pragmatic, low-cost, low-latency choice.

---

## TL;DR
**Start on a Vultr Johannesburg VM (2–4 GB, $12–24/mo) running our `docker-compose`,
with Cloudflare R2 (free) for documents and Caddy for free HTTPS. ~40 ms to
Kigali, ~$15–30/mo all-in.** When you outgrow it, move Postgres→Supabase Pro and
Redis→Upstash and run the API on Fly.io `jnb` machines.
