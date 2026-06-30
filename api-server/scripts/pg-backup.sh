#!/usr/bin/env bash
#
# pg-backup.sh — encrypted daily Postgres backup → Cloudflare R2.
#
# Dumps the production database from the postgres container, gzips it, encrypts
# it (AES-256) with BACKUP_ENCRYPTION_KEY, uploads to the R2 bucket under a
# db-backups/ prefix, and prunes copies older than BACKUP_RETAIN_DAYS.
#
# Reuses the API's R2 credentials (STORAGE_*) from .env, so no new secrets are
# needed except BACKUP_ENCRYPTION_KEY. Designed to run from cron on the box.
#
# Restore: see pg-restore.md.
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/rides/Rides-api/api-server}"
PG_CONTAINER="${PG_CONTAINER:-api-server-postgres-1}"
PREFIX="${BACKUP_PREFIX:-db-backups}"
RETAIN="${BACKUP_RETAIN_DAYS:-14}"
AWSCLI_IMAGE="${AWSCLI_IMAGE:-amazon/aws-cli:latest}"

cd "$APP_DIR"
# Load DB + R2 creds (STORAGE_*, POSTGRES_*, BACKUP_ENCRYPTION_KEY) from .env.
set -a; . ./.env; set +a

: "${POSTGRES_USER:?missing}"; : "${POSTGRES_DB:?missing}"
: "${STORAGE_BUCKET:?missing}"; : "${STORAGE_ENDPOINT:?missing}"
: "${STORAGE_KEY_ID:?missing}"; : "${STORAGE_SECRET:?missing}"

STAMP="$(date -u +%Y%m%d-%H%M%S)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
DUMP="$TMP/rideplatform-$STAMP.sql.gz"

# 1. Dump (no-owner so it restores cleanly into a fresh role) + compress.
docker exec "$PG_CONTAINER" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner \
  | gzip -9 > "$DUMP"
SIZE="$(stat -c%s "$DUMP" 2>/dev/null || stat -f%z "$DUMP")"
if [ "${SIZE:-0}" -lt 1000 ]; then
  echo "ERROR: dump suspiciously small ($SIZE bytes) — aborting, not uploading" >&2
  exit 1
fi

# 2. Encrypt if a key is configured (strongly recommended — the dump is all of
#    your user + payment data). Without a key it uploads plaintext with a warning.
UPLOAD="$DUMP"
if [ -n "${BACKUP_ENCRYPTION_KEY:-}" ]; then
  ENC="$DUMP.enc"
  openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt \
    -in "$DUMP" -out "$ENC" -pass env:BACKUP_ENCRYPTION_KEY
  UPLOAD="$ENC"
else
  echo "WARNING: BACKUP_ENCRYPTION_KEY not set — uploading UNENCRYPTED" >&2
fi
OBJ="$PREFIX/$(basename "$UPLOAD")"

# 3. Upload to R2 (S3-compatible) via a throwaway aws-cli container — no host install.
aws_r2() {
  docker run --rm \
    -e AWS_ACCESS_KEY_ID="$STORAGE_KEY_ID" \
    -e AWS_SECRET_ACCESS_KEY="$STORAGE_SECRET" \
    -e AWS_DEFAULT_REGION="${STORAGE_REGION:-auto}" \
    -v "$TMP:/data" "$AWSCLI_IMAGE" \
    --endpoint-url "$STORAGE_ENDPOINT" "$@"
}
aws_r2 s3 cp "/data/$(basename "$UPLOAD")" "s3://$STORAGE_BUCKET/$OBJ"
echo "OK: uploaded s3://$STORAGE_BUCKET/$OBJ (pre-encrypt $SIZE bytes)"

# 4. Prune remote backups older than RETAIN days (filenames carry a sortable UTC stamp).
CUTOFF="$(date -u -d "@$(( $(date +%s) - RETAIN*86400 ))" +%Y%m%d-%H%M%S 2>/dev/null \
        || date -u -v-"${RETAIN}"d +%Y%m%d-%H%M%S)"
aws_r2 s3 ls "s3://$STORAGE_BUCKET/$PREFIX/" 2>/dev/null | awk '{print $4}' | while read -r f; do
  [ -n "$f" ] || continue
  st="$(printf '%s' "$f" | grep -oE '[0-9]{8}-[0-9]{6}' || true)"
  if [ -n "$st" ] && [ "$st" \< "$CUTOFF" ]; then
    aws_r2 s3 rm "s3://$STORAGE_BUCKET/$PREFIX/$f" >/dev/null && echo "pruned $f"
  fi
done
echo "Backup complete."
