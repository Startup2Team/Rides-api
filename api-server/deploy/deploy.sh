#!/usr/bin/env bash
# deploy.sh — promote a pre-built GHCR image into an environment on this box.
#
#   ./deploy/deploy.sh <staging|prod> <image_tag>
#
# It pulls the exact image (no build), starts the api (DB migrations run on
# boot), waits for /health to pass, and AUTO-ROLLS-BACK to the previously
# deployed tag if the health gate fails. Idempotent and safe to re-run.
#
# The image tag is normally the full git SHA (immutable), matching the existing
# GHCR package convention. The previously deployed tag is remembered in a state
# file so rollback is one command.
set -euo pipefail

ENV="${1:?usage: deploy.sh <staging|prod> <image_tag>}"
TAG="${2:?usage: deploy.sh <staging|prod> <image_tag>}"

REGISTRY="ghcr.io/startup2team/rides-api"
# Override with RIDES_DIR for local rehearsals / testing; defaults to the box path.
DIR="${RIDES_DIR:-/opt/rides/Rides-api/api-server}"
cd "$DIR"

case "$ENV" in
  staging)
    # --env-file is REQUIRED: compose interpolates ${POSTGRES_USER} etc. from the
    # env file passed here (or the default .env), NOT from a service's env_file.
    # Without it, the staging stack would interpolate prod values.
    COMPOSE=(docker compose --env-file .env.staging -p rides-staging -f docker-compose.staging.yml)
    STATE="$DIR/.staging_current_tag"
    ENVFILE="$DIR/.env.staging" ;;
  prod)
    COMPOSE=(docker compose --env-file .env -f docker-compose.prod.yml)
    STATE="$DIR/.prod_current_tag"
    ENVFILE="$DIR/.env" ;;
  *) echo "unknown env: $ENV (expected staging|prod)" >&2; exit 2 ;;
esac

log() { echo "[$(date -u +%FT%TZ)] [$ENV] $*"; }

# Mirror the deployed tag into the env file.
#
# This script exports API_IMAGE_TAG for its own compose invocations, but anyone
# running `docker compose up -d api` by hand gets the value from the env file
# instead — and compose falls back to `:-dev` when it is absent or stale. That
# is not a theoretical problem: on 2026-07-26 a manual restart picked up a tag
# left over from an earlier deploy and rolled staging back to an image whose
# migrations stopped at 074 while the database was at 76, crash-looping it.
# Keeping the env file in step means both paths always start the same image.
persist_tag() {
  local tag="$1"
  if [ ! -f "$ENVFILE" ]; then
    log "WARN: $ENVFILE missing — cannot persist API_IMAGE_TAG"
    return 0
  fi
  # Rewrite via a temp file rather than `sed -i`, whose in-place flag differs
  # between GNU and BSD sed — this script also runs locally via RIDES_DIR.
  # Write the temp file alongside the target so the mv stays on one filesystem.
  if grep -q '^API_IMAGE_TAG=' "$ENVFILE"; then
    local tmp="$ENVFILE.tag.$$"
    if ! grep -v '^API_IMAGE_TAG=' "$ENVFILE" > "$tmp"; then
      rm -f "$tmp"; log "WARN: could not rewrite $ENVFILE — API_IMAGE_TAG left unchanged"; return 0
    fi
    printf 'API_IMAGE_TAG=%s\n' "$tag" >> "$tmp"
    chmod --reference="$ENVFILE" "$tmp" 2>/dev/null || chmod 600 "$tmp"
    mv -f "$tmp" "$ENVFILE"
  else
    printf '\n# Written by deploy.sh — the image tag currently deployed.\n# Keeps a manual `docker compose up` from starting a different image.\nAPI_IMAGE_TAG=%s\n' "$tag" >> "$ENVFILE"
  fi
  log "env file pinned to API_IMAGE_TAG=$tag"
}

PREV="$(cat "$STATE" 2>/dev/null || echo latest)"
log "current=$PREV  target=$TAG"

deploy_tag() {
  local tag="$1"
  export API_IMAGE_TAG="$tag"
  log "pulling $REGISTRY:$tag"
  if ! "${COMPOSE[@]}" pull api; then
    # Fall back to a locally-present copy (e.g. transient registry blip, or a
    # rollback to a tag already on the box). Abort only if it isn't available.
    if docker image inspect "$REGISTRY:$tag" >/dev/null 2>&1; then
      log "registry pull failed — using local image $REGISTRY:$tag"
    else
      log "ERROR: cannot pull $REGISTRY:$tag and no local copy exists"
      return 1
    fi
  fi
  log "starting api (migrations run on boot)"
  "${COMPOSE[@]}" up -d --no-build api
  # Persist only after the container actually started, so a failed pull never
  # leaves the env file pointing at an image that was never deployed. Rollback
  # calls this too, so the file always names what is really running.
  persist_tag "$tag"
}

health_ok() {
  # Curl the container's own /health (DB + Redis probe). No host port needed.
  for _ in $(seq 1 40); do
    if "${COMPOSE[@]}" exec -T api curl -fsS http://localhost:8080/health >/dev/null 2>&1; then
      return 0
    fi
    sleep 3
  done
  return 1
}

deploy_tag "$TAG"

log "waiting for /health ..."
if health_ok; then
  echo "$TAG" > "$STATE"
  log "DEPLOY OK — $ENV now running $TAG"
  "${COMPOSE[@]}" exec -T api curl -fsS http://localhost:8080/health || true
  echo
  docker image prune -f >/dev/null 2>&1 || true
  exit 0
fi

log "HEALTH CHECK FAILED for $TAG — rolling back to $PREV"
deploy_tag "$PREV"
if health_ok; then
  log "ROLLED BACK — $ENV restored to $PREV"
else
  log "CRITICAL: rollback to $PREV also failed — manual intervention required"
fi
exit 1
