# GHCR image-based deploy (Vultr JNB)

Migrates production from **build-on-box** (`git archive` → `docker compose up --build`)
to **pull-a-prebuilt-image** from the GitHub Container Registry. The box stops
compiling Go; every release is an immutable, roll-back-able image tag.

Image: `ghcr.io/startup2team/rides-api`
Tags: `:latest` (moving) and `:<git-sha>` (immutable — use for rollback).

---

## Pieces (already in the repo)

1. **`.github/workflows/image.yml`** — on push to `main` (or manual dispatch),
   builds the `linux/amd64` image from `api-server/Dockerfile` and pushes
   `:latest` + `:<sha>` to GHCR. Auth uses the built-in `GITHUB_TOKEN`
   (`packages: write`) — **no PAT needed to PUSH from Actions.**
2. **`docker-compose.prod.yml`** — the `api` service now has BOTH `image:` and
   `build:`. Both paths work during migration:
   - `docker compose up -d --build api` → builds locally (legacy).
   - `docker compose pull api && docker compose up -d api` → pulls from GHCR (new).

---

## One-time setup

### 1. Produce the first image
Merge `image.yml` to `main` (or run it via **Actions → Build & Push Image →
Run workflow**). Confirm the package appears at
`https://github.com/orgs/Startup2Team/packages`.

### 2. Let the box PULL the image — pick ONE

**Option 1 — make the package public** (simplest; the image bakes NO secrets —
`.env` is injected at runtime, so nothing sensitive ships in it):
GitHub → the `rides-api` package → **Package settings → Change visibility →
Public**. The box then pulls with no login. Done.

**Option 2 — keep it private** (needs a one-time box login):
Create a PAT (classic) with **`read:packages`** only, then on the box:
```bash
echo "<PAT>" | docker login ghcr.io -u <github-username> --password-stdin
```
This persists in `/root/.docker/config.json`, so the box can pull thereafter.

### 3. Verify the box can pull
```bash
ssh rides-prod 'docker pull ghcr.io/startup2team/rides-api:latest && echo PULL_OK'
```

---

## Flip the deploy from build → pull

Only after steps 1–3 succeed. In the **CD workflow** (currently
`cd.yml` on `deploy/vultr-jnb`), replace the "ship + build" step with a
pull-based deploy:

```yaml
      - name: Deploy — pull image + restart api
        run: |
          SSH="ssh -i ~/.ssh/deploy -o BatchMode=yes root@139.84.251.242"
          # Keep tracked files (compose, nginx) in sync — now a ~1.8 MB archive.
          git archive --format=tar HEAD | $SSH 'tar -x -C /opt/rides/Rides-api --overwrite'
          $SSH "cd /opt/rides/Rides-api/api-server && \
                export API_IMAGE_TAG=${{ github.sha }} && \
                docker compose -f docker-compose.prod.yml pull api && \
                docker compose -f docker-compose.prod.yml up -d api && \
                docker image prune -f"
```

Note: we still `git archive` (cheap now) to sync compose/nginx, but the `api`
service is **pulled**, not built. To pin the exact release, persist the tag on
the box (e.g. write `API_IMAGE_TAG=<sha>` into `.env`) or export it inline as above.

---

## Rollback (deterministic, seconds)

```bash
ssh rides-prod 'cd /opt/rides/Rides-api/api-server && \
  export API_IMAGE_TAG=<older-git-sha> && \
  docker compose -f docker-compose.prod.yml pull api && \
  docker compose -f docker-compose.prod.yml up -d api'
```

---

## Notes / gotchas
- **Arch:** the image is built `linux/amd64` to match the box. GitHub runners
  are amd64, so this is a native build (fast). Building on an Apple-Silicon Mac
  would need `docker buildx --platform linux/amd64` — avoid; let CI do it.
- **admin service** still builds from the Rides-web repo on the box — unrelated
  to this change; only `api` moved to a pulled image.
- **Branch tangle:** `main` still carries a dead Railway `cd.yml`; the live CD is
  on `deploy/vultr-jnb`. Consolidate the CD onto `main` when flipping, so
  push-to-main is the single deploy trigger. See [[deploy-and-ops]].
