#!/usr/bin/env bash
# osrm-prep.sh — ONE-TIME data prep for OSRM Rwanda routing (MLD pipeline).
#
# Downloads the Rwanda OpenStreetMap extract and runs
#   osrm-extract -> osrm-partition -> osrm-customize
# producing /opt/rides/osrm-data/rwanda/rwanda-latest.osrm(.partition/.cells/
# .mldgr/...) for the `osrm` compose service (docker-compose.osrm.yml) to
# serve read-only via `osrm-routed --algorithm mld`.
#
# Run ONCE on the box, manually, BEFORE the first
#   docker compose ... -f docker-compose.osrm.yml up -d osrm
# Safe to re-run (idempotent: re-downloads, re-derives from scratch) whenever
# the Rwanda extract needs refreshing (new roads, OSM edits).
#
# Designed so a second country is a config add, not a redesign: copy this
# script, change PBF_URL/DATA_DIR/COUNTRY, and the compose service just needs
# a second `command:`/volume pointed at the new .osrm file (or a second osrm
# service entirely if both must be served concurrently).
#
# COST NOTE (stated per DevOps task constraints, not independently load-
# tested): osrm-extract/partition/customize are CPU/RAM-heavy relative to
# serving. Rwanda is a small country (~26k km^2 / a few hundred MB of OSM
# data) so this is expected to take minutes, not hours. Pacifique confirms
# prod currently carries ~0 real user traffic (2026-08), so a brief CPU spike
# here is low-risk today — but it DOES compete for the same 2 vCPUs as
# postgres/redis/api, so avoid running this during any future real traffic
# window. Memory is hard-capped below so this cannot OOM the box regardless
# of how the box's traffic looks when this is next re-run.
#
# Usage: ./osrm-prep.sh

set -euo pipefail

DATA_DIR="/opt/rides/osrm-data/rwanda"
PBF_URL="https://download.geofabrik.de/africa/rwanda-latest.osm.pbf"
IMAGE="osrm/osrm-backend:v5.25.0"   # MUST match the image tag in docker-compose.osrm.yml (v5.25.0 = newest published Docker Hub tag, verified 2026-08-31)
PROFILE="/opt/car.lua"              # ships inside the official osrm-backend image

# Caps preprocessing so it can never starve the always-on stack (postgres,
# redis, api, nginx, admin x2) sharing this box's 2 vCPUs / 3.8GB RAM.
DOCKER_LIMITS=(--memory=2g --memory-swap=4g --cpus=1.5)

mkdir -p "$DATA_DIR"
cd "$DATA_DIR"

echo "[1/4] Downloading Rwanda extract from Geofabrik..."
curl -fL -o rwanda-latest.osm.pbf "$PBF_URL"
ls -lh rwanda-latest.osm.pbf

echo "[2/4] osrm-extract (car profile, MLD-compatible)..."
docker run --rm "${DOCKER_LIMITS[@]}" -v "$DATA_DIR:/data" "$IMAGE" \
  osrm-extract -p "$PROFILE" /data/rwanda-latest.osm.pbf

echo "[3/4] osrm-partition..."
docker run --rm "${DOCKER_LIMITS[@]}" -v "$DATA_DIR:/data" "$IMAGE" \
  osrm-partition /data/rwanda-latest.osrm

echo "[4/4] osrm-customize..."
docker run --rm "${DOCKER_LIMITS[@]}" -v "$DATA_DIR:/data" "$IMAGE" \
  osrm-customize /data/rwanda-latest.osrm

echo
echo "Done. Files in $DATA_DIR:"
ls -lh "$DATA_DIR"
echo
echo "Next: deploy the osrm service — see OSRM_DEPLOY.md step 4."
