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
    STATE="$DIR/.staging_current_tag" ;;
  prod)
    COMPOSE=(docker compose --env-file .env -f docker-compose.prod.yml)
    STATE="$DIR/.prod_current_tag" ;;
  *) echo "unknown env: $ENV (expected staging|prod)" >&2; exit 2 ;;
esac

log() { echo "[$(date -u +%FT%TZ)] [$ENV] $*"; }

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
