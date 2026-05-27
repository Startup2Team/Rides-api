# Deployment Guide

## Deployment Goals

As DevOps owner, your job is to make releases:

- Repeatable.
- Observable.
- Easy to roll back.
- Safe with migrations and secrets.
- Consistent across local, staging, and production.

## Environments

| Environment | Purpose | Data |
|---|---|---|
| Local | Developer testing with Docker Compose. | Disposable local data. |
| Staging | Production-like validation before release. | Seeded test data. |
| Production | Real users. | Protected live data. |

## Local Deployment

From `artifacts/api-server`:

```bash
make env
make db-up
make dev
```

Full local stack:

```bash
make docker-up
```

Local services:

| Service | Port |
|---|---:|
| API | 8080 |
| Postgres/PostGIS | 5432 |
| Redis | 6379 |
| MinIO API | 9000 |
| MinIO Console | 9001 |

## Production Runtime Components

| Component | Production Recommendation |
|---|---|
| API | Container platform: Render, Fly.io, ECS, Kubernetes, Railway, or similar. |
| Postgres | Managed PostgreSQL with PostGIS extension enabled. |
| Redis | Managed Redis with persistence/backups where supported. |
| Object storage | S3-compatible bucket + CDN. |
| Secrets | Provider secret manager or encrypted CI/CD environment variables. |
| Logs | Centralized logs with retention. |
| Metrics | HTTP latency, error rate, DB/Redis health, matching failures. |

## Required Environment Variables

Minimum:

```env
PORT=8080
ENV=production
DATABASE_URL=postgres://...
REDIS_URL=redis://...
JWT_ACCESS_SECRET=...
JWT_REFRESH_SECRET=...
```

Optional but expected for production:

```env
AT_API_KEY=...
AT_USERNAME=...
AT_SENDER_ID=...
AT_MASKING_NUMBER=...
FIREBASE_SERVICE_ACCOUNT_PATH=...
MOMO_API_KEY=...
MOMO_SUBSCRIPTION_KEY=...
MOMO_ENVIRONMENT=...
```

## Release Steps

1. Merge code into main branch.
2. CI runs tests, coverage, Swagger validation, Docker build.
3. Build and tag container image.
4. Push image to registry.
5. Deploy to staging.
6. Run migrations against staging.
7. Run smoke tests.
8. Promote image to production.
9. Run migrations against production.
10. Run production smoke tests.
11. Watch logs and metrics.

## Migration Strategy

Current app runs migrations on startup.

For MVP this is acceptable, but production should move toward a controlled migration job:

```text
deploy migration job -> verify -> deploy app
```

Rules:

- Migrations must be backward-compatible where possible.
- Avoid destructive migrations during normal deploys.
- Prefer add-column, deploy code, backfill, then remove old column later.
- Always keep `.down.sql` for local and staging rollback.

## Rollback Strategy

Rollback order:

1. Stop or pause new deploy.
2. Roll back API container to previous image.
3. Check whether migration rollback is safe.
4. If migration is additive, usually leave DB as-is.
5. If migration breaks old app, run down migration only after confirming no data loss risk.
6. Verify health and critical endpoints.

## Health Checks

Current health endpoint:

```text
GET /health
```

Smoke commands:

```bash
curl -fsS https://api.example.com/health
curl -fsS https://api.example.com/swagger/openapi.json >/dev/null
```

## Monitoring Checklist

Add dashboards for:

- HTTP request count.
- HTTP 4xx/5xx rate.
- p95 and p99 latency.
- Postgres connection count.
- Redis memory and command latency.
- Matching failures/no-driver-found.
- GPS anomalies.
- OTP send failures.
- WebSocket connection count.
- Ride cancellation rate.

## Logs

The app uses zerolog. Production logs should be structured JSON.

Important fields:

- `request_id`
- `user_id`
- `ride_id`
- `driver_id`
- `status`
- `error`
- `event_type`

## Security Checklist

- Use strong JWT secrets.
- Rotate secrets periodically.
- Restrict database network access.
- Use TLS for API and managed services.
- Do not expose Swagger publicly in production unless protected.
- Restrict WebSocket origins in production.
- Back up Postgres.
- Enable Redis auth/TLS if managed provider supports it.
- Store Firebase service account securely.

## Production Readiness Gaps

- Add metrics/tracing.
- Make WebSocket delivery cluster-safe.
- Add integration tests before production release.
- Add admin document review.
- Add signed upload URL flow for document storage.
- Protect or disable Swagger in production.
