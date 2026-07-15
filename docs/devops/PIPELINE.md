# Rides — Development & Delivery Pipeline

How code goes from your laptop to production **safely and reproducibly**. The
core promise: the artifact tested in CI is the *exact same image* that runs in
staging and then in production — no rebuild, no "works on my machine" surprises.

```
 local  ──PR──▶  dev  ──release──▶  main / prod
  │              │                    │
  │   CI gate    │   auto-deploy      │  manual-approval deploy
  │  (lint/test/ │   to STAGING       │  of the SAME image,
  │   docker)    │   + smoke test     │  + smoke test + auto-rollback
```

---

## 1. Environments

| Env | Where | URL | DB | Deploys when |
|-----|-------|-----|----|----|
| **local** | your machine | `localhost:8080` | local docker | you run it |
| **staging** | Vultr box, `rides-staging` stack, `:8090` | `stg-api.rides.rw` | `rides_stg` (own Postgres/Redis) | every merge to `dev` (auto) |
| **production** | Vultr box, prod stack, `:8080` | `api.rides.rw` | `rides_prod` | you publish a Release **and approve** |

Staging is fully isolated from prod (own containers, volumes, database) so a bad
migration or data reset on staging can never touch prod.

## 2. Branching model

- `feature/*`, `fix/*` → open a **PR into `dev`**. CI must be green + 1 review.
- `dev` is the integration/staging branch. Merging to it auto-deploys to staging.
- `main` is production truth. You only reach it via a **PR from `dev` → `main`**.
- Production ships from a **GitHub Release** (`vX.Y.Z`) cut on `main`.

Direct pushes to `dev`/`main` are blocked; history is linear (squash/rebase).

## 3. The workflows (`.github/workflows/`)

| File | Trigger | Does |
|------|---------|------|
| `ci.yml` | every PR + push to dev/main | Lint (vet+gofmt), Test (`-race`+coverage), **Docker Build** (real image, no push). The merge gate. |
| `build-image.yml` | push to dev/main, tag `v*` | Builds **one immutable image** `ghcr.io/…/rides-api:<sha>` (+ moving tags). |
| `deploy-staging.yml` | after Build Image succeeds on `dev` | Deploys `<sha>` to the staging stack, migrates, smoke-tests. |
| `deploy-prod.yml` | Release published | **Waits for manual approval** (production environment), promotes the same `<sha>`, smoke-tests, **auto-rolls-back** on failure. |

Why "Docker Build" is a required check: `go build` on a dev machine uses your
local Go; only the container build exercises the pinned base image + `go mod
download`. That check is what catches toolchain/dependency drift before merge.

## 4. Day-to-day flow

**Ship a change**
1. Branch off `dev`: `git checkout -b fix/thing dev`
2. Locally: `make fmt && go vet ./... && go test ./...` and **build the container** if you touched `go.mod`/`Dockerfile`: `docker build api-server` (see §7).
3. Push, open a PR into `dev`. CI runs; get a review; merge.
4. Merge auto-deploys to **staging**. Verify at `https://stg-api.rides.rw/health` and exercise the change.

**Promote to production**
5. Open a PR `dev → main`, merge when staging looks good.
6. Cut a release: `gh release create v1.4.0 --target main --generate-notes`
7. `deploy-prod` starts and **pauses for approval** → approve it in the Actions tab.
8. It promotes the exact image, migrates, smoke-tests. If `/health` fails it
   auto-rolls-back to the previous release and the job goes red.

**Roll back manually** (on the box):
```bash
cd /opt/rides/Rides-api/api-server
cat .prod_current_tag          # see what's live
./deploy/deploy.sh prod <previous-good-sha>
```

## 5. Logging & observability

- **CI**: each job streams to the Actions log; coverage + pushed image tags land
  in the run **Summary**.
- **Deploys**: `deploy/deploy.sh` prints timestamped `[env]` lines (pull →
  start → health → ok/rollback) — visible in the Actions log.
- **Runtime**: the API logs structured JSON (zerolog). On the box:
  `docker compose -f docker-compose.prod.yml logs -f api`.
- **Health**: `/health` probes Postgres + Redis and returns 503 if either is
  down; `/metrics` exposes HTTP/DB/Redis/WS/queue gauges.
- **Alerts**: Telegram (see the telegram-alerting workflow/branches).

## 6. Replicating prod locally (avoid environment drift)

Same images, same Postgres/Redis versions everywhere. To run a prod-like stack
locally, pull the built image instead of building:
```bash
cd api-server
export API_IMAGE_TAG=<sha>           # any tag from GHCR
docker compose -f docker-compose.prod.yml pull api
docker compose -f docker-compose.prod.yml up -d
curl -fsS localhost:8080/health
```
Because staging and prod deploy this same image + run the same migrations on
boot, behavior matches. Never hand-edit anything on the box that isn't in git —
config lives in `.env` / `.env.staging` (secrets) and everything else is tracked.

## 7. When to build the container locally (the rule that would have saved a red CI)

Run `docker build api-server` before pushing **whenever you change**:
`go.mod` / `go.sum`, the `Dockerfile`, the Go toolchain, or add a dependency.
`go test` alone won't catch a base-image/toolchain mismatch — the container build will.

## 8. One-time setup

**GitHub** (run once, as an org admin):
```bash
gh auth login
REVIEWER=<your-login> ./scripts/setup-github-pipeline.sh
```
Then add the `DEPLOY_SSH_KEY` secret (Settings → Secrets → Actions).

**The box** (once):
```bash
# The rides-api package is PUBLIC → the box pulls with NO login.
# (Only if you later make it private: docker login ghcr.io -u <user> -p <PAT read:packages>)
# shared network so prod nginx can reach the staging api
docker network create rides-edge
# staging config
cd /opt/rides/Rides-api/api-server
cp .env .env.staging   # then edit: POSTGRES_DB=rides_stg, own secrets, PAYMENTS_ENABLED=false
```

**nginx** (once) — add a staging server block and put prod nginx on the shared
network so it can resolve the staging container:
```nginx
# in nginx/nginx.conf, alongside the api.rides.rw server:
server {
    listen 443 ssl; http2 on;
    server_name stg-api.rides.rw;
    ssl_certificate     /etc/nginx/certs/rides.rw.pem;   # wildcard *.rides.rw
    ssl_certificate_key /etc/nginx/certs/rides.rw.key;
    location / {
        proxy_pass         http://rides-staging-api:8080;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;
        proxy_set_header   Connection $connection_upgrade;
        proxy_set_header   Host $host;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto https;
        proxy_read_timeout 3600s;
    }
}
```
Add `stg-api.rides.rw` to the `:80` redirect `server_name`, attach the prod
`nginx` service to the external `rides-edge` network in `docker-compose.prod.yml`,
add a Cloudflare DNS record for `stg-api`, then `docker compose … up -d nginx`.

## 9. Safety guarantees (why this won't kill prod)

- Prod only deploys a **release you explicitly cut** + **a human approval**.
- It deploys an image that **already passed CI and ran on staging** — same bytes.
- Migrations are validated on staging first (same migration chain).
- Failed smoke test → **automatic rollback** to the previous release.
- DB/Redis are loopback-only; staging is isolated from prod data.
- Branch protection blocks unreviewed or CI-failing code from `main`.
