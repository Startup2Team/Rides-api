# File Structure and Shared Components

## Repository Layout

```text
artifacts/api-server/
├── cmd/
│   └── server/
│       └── main.go
├── config/
│   ├── config.go
│   └── openapi.json
├── docs/
├── internal/
│   ├── admin/
│   ├── analytics/
│   ├── api_test/
│   ├── auth/
│   ├── customer/
│   ├── driver/
│   ├── e2e_test/
│   ├── location/
│   ├── matching/
│   ├── middleware/
│   ├── negotiation/
│   ├── notification/
│   ├── payment/
│   ├── ride/
│   ├── telephony/
│   └── tracking/
├── migrations/
├── pkg/
│   ├── errors/
│   ├── geo/
│   ├── logger/
│   ├── postgres/
│   ├── redis/
│   └── respond/
├── Dockerfile
├── docker-compose.yml
├── Makefile
├── go.mod
└── go.sum
```

## Entry Point

`cmd/server/main.go`

Responsibilities:

- Load config.
- Connect Postgres.
- Connect Redis.
- Run migrations.
- Build services and handlers.
- Register routes.
- Start background analytics consumer.
- Start HTTP server.
- Gracefully shut down.

## Config

`config/config.go`

Reads environment variables and builds `Config`.

Important:

- `DATABASE_URL` is required.
- `JWT_ACCESS_SECRET` is required.
- `JWT_REFRESH_SECRET` is required.
- Redis has a localhost default.
- Provider keys can be blank in local development.

`config/openapi.json`

Swagger contract served at:

```text
/swagger/openapi.json
```

## Internal Modules

### auth

Files:

- `handler.go`
- `service.go`
- `repository.go`

Owns:

- OTP generation and verification.
- User creation/login.
- JWT token issuing.
- Refresh sessions.
- Device session logging.

### driver

Owns:

- Driver applications.
- Driver documents.
- Driver policy acceptance.
- Driver availability.
- Location updates and GPS anomaly checks.
- Driver earnings and stats.
- Nearby driver lookup.

### ride

Owns:

- Ride state machine.
- Customer ride creation/cancellation/listing.
- Driver en-route/arrival/start/complete.
- Pickup expiry and no-show cancellation.
- Redis cleanup when rides complete/cancel.

### matching

Owns:

- Candidate search.
- Candidate scoring.
- Sequential driver offers.
- Redis matching locks.
- Driver acceptance/decline notification.

### negotiation

Owns:

- Fare proposal rounds.
- Offer limits.
- Accepting offers.
- Manual fare lock.
- Masked call initiation.

### location

Owns:

- Saved locations.
- Landmarks.
- Suggestions.
- Route cache.
- Agreed fare aggregation into route cache.
- Mode switching.

### tracking

Owns:

- WebSocket client registration.
- Driver/customer message delivery.
- Driver location update reads over WebSocket.

### admin

Owns:

- Driver approval/rejection/suspension.
- User suspension.
- GPS anomaly reads.
- Device collision reads.
- Ride audit listing.

### analytics

Owns:

- Analytics event publishing.
- Redis stream consumer.
- Analytics read endpoints.

## Shared Packages

### pkg/errors

Defines typed HTTP-aware errors:

```go
type AppError struct {
    StatusCode int
    Code string
    Message string
}
```

Use this when services need to return predictable API errors.

### pkg/respond

Central response helpers:

- `OK`
- `Created`
- `NoContent`
- `Error`
- `ErrorMsg`

All handlers should use this package.

### pkg/redis

Single source of truth for Redis key patterns.

Do not create raw Redis key strings in feature code if a key builder belongs here.

### pkg/geo

Geospatial helpers:

- Coordinate validation.
- WKT conversion for PostGIS.
- Haversine distance.
- Speed calculation.

### pkg/postgres

Builds pgx connection pool.

### pkg/logger

Builds zerolog logger.

## Naming Conventions

| Thing | Convention |
|---|---|
| Go packages | lowercase domain names |
| Handlers | HTTP-facing methods |
| Services | business verbs |
| Repositories | SQL/data verbs |
| Redis keys | functions on `redis.Keys` |
| Migrations | `NNN_description.up.sql` and `.down.sql` |
| Endpoint params | snake case, e.g. `{ride_id}` |
| JSON fields | snake case |

## Adding a New Domain Module

Use this structure:

```text
internal/newdomain/
├── handler.go
├── service.go
├── repository.go
└── service_test.go
```

Then:

1. Add constructor calls in `cmd/server/main.go`.
2. Register routes under `/api/v1`.
3. Add migrations if persistent data is needed.
4. Add Swagger paths.
5. Add tests.
6. Update docs.
