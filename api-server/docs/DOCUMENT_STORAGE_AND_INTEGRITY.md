# Document Storage & KYC Integrity

How driver documents and profile photos are stored, why the bucket no longer
needs to be public, and what is currently missing before the KYC record can be
trusted as evidence.

Written 2026-07-26, after the incident where every upload had been failing
silently against a revoked R2 credential.

---

## 1. How storage works now

```
mobile app                       API                        Cloudflare R2
    |                             |                              |
    |-- POST /uploads/presigned-url -->                          |
    |<-- {upload_url, file_url} --|                              |
    |                             |                              |
    |-- PUT bytes ------------------------------------------->  (private bucket)
    |                             |                              |
    |-- POST /driver/documents {file_url} -->  driver_documents  |
    |                             |                              |
 admin / driver views:            |                              |
    |-- GET /api/v1/uploads/objects/documents/<key> -->  streams from R2
```

Writes go **straight to R2** with a short-lived presigned URL (5 min). Reads go
**through the API**, which streams the object back. Object keys are 128-bit
random.

Relevant config:

| Variable | Value (staging) |
| --- | --- |
| `STORAGE_PROVIDER` | `r2` |
| `STORAGE_BUCKET` | `rides-docs` |
| `STORAGE_ENDPOINT` | `https://<account>.r2.cloudflarestorage.com` |
| `STORAGE_CDN_URL` | `https://stg-api.rides.rw/api/v1/uploads/objects` |

`STORAGE_CDN_URL` is what gets written into `driver_documents.file_url` and
`users.profile_image_url`. Pointing it at the API route is what removes the need
for a public bucket.

---

## 2. Turning off public access — what does and does not break

### Does NOT break: database backups

This was the original reason for enabling public access, and it was never
needed. `scripts/pg-backup.sh` talks to R2 over the **S3 API with the
credential**, not over public HTTP:

```sh
aws_r2() {
  docker run --rm \
    -e AWS_ACCESS_KEY_ID="$STORAGE_KEY_ID" \
    -e AWS_SECRET_ACCESS_KEY="$STORAGE_SECRET" \
    --endpoint-url "$STORAGE_ENDPOINT" "$@"
}
aws_r2 s3 cp "/data/$(basename "$UPLOAD")" "s3://$STORAGE_BUCKET/$OBJ"
```

Upload, listing, pruning and restore all authenticate with `STORAGE_KEY_ID` /
`STORAGE_SECRET`. Public access is irrelevant to every one of them.

> Right now `https://rides.rw/db-backups/<file>.sql.gz.enc` returns **200** to
> anyone. The files are encrypted, so this is not an immediate breach, but the
> only thing standing between an attacker and your entire database is
> `BACKUP_ENCRYPTION_KEY`. That is not a margin worth keeping.

### Does NOT break: admin review or the driver's own document list

Both read `file_url`, which is now an API URL. The API fetches the object from
R2 using its credential and streams it back. Verified end-to-end on staging:
presign 200 → PUT 200 → GET 200 with byte-identical content.

### Does break: nothing else

`https://rides.rw/` currently returns a 404 from the bucket — there is no
website behind that hostname. Confirm that is still true before removing the
custom domain (see step 3), in case a marketing site has been pointed at it
since.

---

## 3. Cloudflare steps

Dashboard → **R2 Object Storage** → **rides-docs** → **Settings**.

1. **Check nothing depends on `rides.rw` first.**
   ```sh
   curl -sI https://rides.rw/ | head -3
   ```
   A `404` from R2 means only the bucket is served there. If you ever want
   `rides.rw` for a marketing site, removing it here is a prerequisite anyway.

2. **Public Development URL → Disable.**
   Kills `https://pub-8e8772f2ce8849dd91f86f2d27d1a24d.r2.dev`.

3. **Custom Domains → `rides.rw` → ⋯ → Remove.**
   Kills public reads via `https://rides.rw/<key>`.

4. **Verify it is closed:**
   ```sh
   curl -s -o /dev/null -w "%{http_code}\n" https://rides.rw/db-backups/<known-file>
   curl -s -o /dev/null -w "%{http_code}\n" https://pub-8e8772f2ce8849dd91f86f2d27d1a24d.r2.dev/<known-file>
   ```
   Both should stop returning 200.

5. **Verify the app still works** (this is the one that matters):
   ```sh
   curl -s -o /dev/null -w "%{http_code}\n" https://stg-api.rides.rw/api/v1/uploads/objects/documents/<key>
   ```
   Should be 200 — it reads through the credential, not the public path.

6. **CORS** currently allows `http://localhost:3000` and
   `https://admin.rides.rw` with GET/HEAD/PUT/POST. Browsers only hit R2
   directly for presigned PUTs from the admin panel. The mobile app is not
   subject to CORS. Leave as-is.

### Rotating the credential later

The old key was revoked without anyone noticing, and uploads failed silently for
weeks. When you rotate:

1. Create an **Account API token** (not a User token — User tokens die when the
   person leaves the org), scoped to `rides-docs`, **Object Read & Write**.
2. Update `STORAGE_KEY_ID` / `STORAGE_SECRET` in `.env.staging` and `.env` on
   the server.
3. Restart, **pinning the image tag explicitly** — see the warning in §6.
4. Smoke-test with a real presign → PUT → GET before walking away.

---

## 4. Can a driver change their documents after approval?

**Yes — silently, and with no way to prove what was originally approved.**
Three separate gaps stack up.

### Gap 1 — documents are overwritten in place, with no history

`internal/driver/repository.go`:

```sql
INSERT INTO driver_documents (driver_id, document_type, file_url)
VALUES ($1, $2, $3)
ON CONFLICT (driver_id, document_type)
DO UPDATE SET file_url = EXCLUDED.file_url, uploaded_at = NOW()
```

There is a unique index on `(driver_id, document_type)`, so a re-upload
**replaces** the row. The previous `file_url` is gone from the database. The old
object still sits in R2, but nothing references it any more, so in practice it
is unrecoverable.

The only trace that anything changed is `uploaded_at` moving forward — and
nothing reads it for that purpose.

### Gap 2 — no approval gate on re-upload

`Service.UploadDocument` does not look at `approval_status`:

```go
func (s *Service) UploadDocument(ctx context.Context, userID, documentType, fileURL string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.UpsertDocument(ctx, profile.ID, documentType, fileURL)
}
```

An `APPROVED` driver can `POST /v1/driver/documents` at any time, swap a
licence, and stay approved and online. Nothing re-opens the review.

### Gap 3 — the approval never records *which file* it approved

Admin decisions are written to the audit log as `document_type` plus a comment:

```go
meta["documents"] = body.Documents  // [{document_type, comment}]
h.audit.Record(ctx, adminID, role, "driver.request_more_info", ...)
```

No `file_url`, no hash, no object key. So even with the audit log in hand you
cannot demonstrate which image a reviewer actually looked at. If a driver swaps
a document after approval, there is no artifact that contradicts them.

**Net effect:** the current KYC record shows the *latest* documents, not the
*approved* ones, and cannot distinguish the two.

---

## 5. Recommended design

Roughly in order of value per unit of work.

### 5.1 Make documents append-only (highest value)

Stop overwriting. Add a version chain instead:

```sql
ALTER TABLE driver_documents ADD COLUMN superseded_at TIMESTAMPTZ;
ALTER TABLE driver_documents ADD COLUMN sha256 CHAR(64);
ALTER TABLE driver_documents ADD COLUMN review_status VARCHAR(20)
  NOT NULL DEFAULT 'PENDING';  -- PENDING | APPROVED | REJECTED

-- the unique index must become partial, so only the live row is constrained
DROP INDEX idx_driver_documents_driver_type;
CREATE UNIQUE INDEX idx_driver_documents_driver_type_live
  ON driver_documents(driver_id, document_type)
  WHERE superseded_at IS NULL;
```

`UpsertDocument` becomes: mark the current row `superseded_at = NOW()`, insert
the new one. You keep the whole history, and "what did we approve on the 4th"
becomes answerable.

### 5.2 Hash every upload, and bind the hash to the decision

Compute SHA-256 of the bytes at upload time (the API already streams them in
proxy mode; for presigned uploads, hash client-side and verify server-side on
the record call, or issue a HEAD to R2 and store the ETag).

Store the hash on the approval decision too. Then "this is the licence the
reviewer approved" is a one-line check rather than an argument.

### 5.3 Re-upload after approval re-opens review

In `UploadDocument`, when the driver is already `APPROVED`:

- set that document's `review_status` back to `PENDING`
- move the driver to a `DOCUMENTS_CHANGED` state (or straight back to
  `PENDING`, depending on how strict you want to be)
- write an audit entry and notify ops

This is the single change that closes the "get approved with real papers, then
swap them" hole. Do it **server-side** — hiding the button in the app is not
enforcement.

### 5.4 Answering "can we make documents view-only?"

Yes, and this is the right default:

- **Once a document is `APPROVED`, the driver app shows it read-only** — no
  replace action.
- **Changing it requires admin to request a re-upload.** That flow already
  exists (`POST /admin/drivers/:id/request-more-info` with per-document
  comments) and it is exactly the right hook: it opens a scoped, time-boxed
  window for one specific document.
- **The API enforces it**: reject `POST /driver/documents` for a document that
  is `APPROVED` and has no open re-upload request. Return a clear error the app
  can render.

That gives you view-only by default, mutable only through an audited,
admin-initiated path.

### 5.5 Consider R2 Bucket Lock for the bytes themselves

The bucket settings already expose **Bucket Lock Rules**. A retention rule on
the `documents/` prefix prevents an object being overwritten or deleted for a
fixed period — including by anyone holding the API credential. Worth enabling
once retention periods are decided, because it protects against a compromised
token as well as a dishonest driver.

Note this interacts with §5.1: append-only writes create *new* keys rather than
replacing old ones, so a lock rule will not fight the application.

### 5.6 Retention and privacy

National IDs, driving licences and selfies are sensitive personal data under
Rwanda's data protection law (Law No. 058/2021 relating to the protection of
personal data and privacy). Two things follow that are worth deciding
deliberately rather than by default:

- **How long** you keep KYC documents after a driver leaves the platform.
  Right now the answer is "forever" — there is no deletion path. An Object
  Lifecycle Rule on `documents/` can enforce whatever you choose.
- **Who can read them.** Reads currently require no authentication — the random
  object key is the only credential (see §7).

Confirm the specific retention and consent obligations with counsel; this
document is not legal advice.

---

## 6. Operational warning: pinning the image tag

`deploy.sh` sets `API_IMAGE_TAG` by `export` at deploy time and does **not**
persist it. A plain `docker compose up -d api` therefore falls back to whatever
is in the env file — which was stale — and silently rolls the API backwards.
That is how staging went down on 2026-07-26: it was rolled back to an image
whose migrations stopped at 074 while the database was at 76, and it
crash-looped on startup.

Both env files now carry a correct `API_IMAGE_TAG`, but treat it as a snapshot,
not a source of truth. Before any manual restart:

```sh
docker inspect <container> --format '{{.Config.Image}}'   # what is actually running
```

and pass that tag explicitly:

```sh
export API_IMAGE_TAG=<sha-from-inspect>
docker compose --env-file .env.staging -p rides-staging -f docker-compose.staging.yml up -d api
```

---

## 7. Known remaining weakness: unauthenticated reads

`GET /api/v1/uploads/objects/*` is deliberately public so `<img src>` works in
the admin panel without forwarding a bearer token. Security rests entirely on
the 128-bit random object key being unguessable.

That is defensible, but it means a leaked URL — a screenshot, a support ticket,
a browser history, a log line — exposes that document permanently, with no way
to revoke it short of deleting the object.

If you want to close this, the shape is: keep the route authenticated, and have
the admin panel proxy image requests through its own Next.js route handler
(which already holds the admin session). The mobile app can send its bearer
token via `expo-image`'s `headers` option. This is a contained change, but it
touches every place a document or avatar is rendered, so it is worth doing
deliberately rather than alongside something else.
