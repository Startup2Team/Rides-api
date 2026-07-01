# Restoring a Postgres backup from R2

Backups are written by `pg-backup.sh` to `s3://<bucket>/db-backups/` as
`rideplatform-<UTC-stamp>.sql.gz.enc` (AES-256, OpenSSL pbkdf2).

> ⚠️ You need `BACKUP_ENCRYPTION_KEY` to decrypt. It lives in the box `.env` —
> **also keep a copy in your password manager off the box**, or a dead box means
> unrecoverable backups.

## 1. Download the chosen backup
```bash
cd /opt/rides/Rides-api/api-server
set -a; . ./.env; set +a
OBJ=db-backups/rideplatform-YYYYMMDD-HHMMSS.sql.gz.enc   # pick from the list below
docker run --rm -e AWS_ACCESS_KEY_ID="$STORAGE_KEY_ID" -e AWS_SECRET_ACCESS_KEY="$STORAGE_SECRET" \
  -e AWS_DEFAULT_REGION="${STORAGE_REGION:-auto}" -v /tmp:/data amazon/aws-cli \
  --endpoint-url "$STORAGE_ENDPOINT" s3 cp "s3://$STORAGE_BUCKET/$OBJ" /data/backup.sql.gz.enc

# list available backups:
#   ... amazon/aws-cli --endpoint-url "$STORAGE_ENDPOINT" s3 ls "s3://$STORAGE_BUCKET/db-backups/"
```

## 2. Decrypt + decompress
```bash
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -in /tmp/backup.sql.gz.enc \
  -out /tmp/backup.sql.gz -pass env:BACKUP_ENCRYPTION_KEY
gunzip /tmp/backup.sql.gz   # -> /tmp/backup.sql
```

## 3. Restore
Restore into a **fresh** database that already has the PostGIS extension
(the dump references PostGIS types):

```bash
# Example: restore into a new DB to verify before swapping.
docker exec -i api-server-postgres-1 psql -U "$POSTGRES_USER" -d postgres \
  -c "CREATE DATABASE rideplatform_restore;"
docker exec -i api-server-postgres-1 psql -U "$POSTGRES_USER" -d rideplatform_restore \
  -c "CREATE EXTENSION IF NOT EXISTS postgis;"
docker exec -i api-server-postgres-1 psql -U "$POSTGRES_USER" -d rideplatform_restore < /tmp/backup.sql
```

To restore over the live DB instead, stop the API first
(`docker compose -f docker-compose.prod.yml stop api`), restore into
`rideplatform`, then start it again.

## Monthly restore test (do this!)
A backup you've never restored is not a backup. Once a month, run steps 1–3 into
`rideplatform_restore` and spot-check a couple of tables, then drop it.
