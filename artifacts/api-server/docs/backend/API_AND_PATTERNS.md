# API and Backend Patterns

## Base URL

```text
http://localhost:8080/api/v1
```

## Response Format

Success with body:

```json
{
  "data": {
    "id": "..."
  }
}
```

Success without body:

```text
204 No Content
```

Error:

```json
{
  "error": {
    "code": "BAD_REQUEST",
    "message": "invalid request"
  }
}
```

Use `pkg/respond` for all handler responses.

## Error Pattern

Use `pkg/errors.AppError` for typed HTTP-aware errors.

Common codes:

| Code | HTTP | Meaning |
|---|---:|---|
| `UNAUTHORIZED` | 401 | Missing/invalid auth. |
| `FORBIDDEN` | 403 | Authenticated but wrong role or suspended. |
| `NOT_FOUND` | 404 | Resource absent or not owned by actor. |
| `CONFLICT` | 409 | State conflict. |
| `INVALID_TRANSITION` | 409 | Ride state transition not allowed. |
| `FARE_LOCKED` | 409 | Fare already immutable. |
| `GEO_FENCE_VIOLATION` | 422 | Driver outside required radius. |
| `RATE_LIMITED` | 429 | Too many requests. |

## Auth Protection

Public endpoints:

- `GET /health`
- `GET /swagger`
- `GET /swagger/openapi.json`
- `POST /api/v1/auth/register`
- `POST /api/v1/auth/verify-otp`
- `POST /api/v1/auth/refresh`
- `GET /api/v1/locations/landmarks`

Authenticated endpoints:

```go
r.Use(mw.Authenticate(cfg, rdb))
```

Role-protected endpoints:

```go
r.Use(mw.RequireRole(mw.RoleDriverActive))
```

Admin endpoints:

```go
r.Use(mw.Authenticate(cfg, rdb))
r.Use(mw.RequireRole(mw.RoleAdmin))
```

## Module Pattern

Each domain module should follow this shape:

```text
internal/{domain}/handler.go
internal/{domain}/service.go
internal/{domain}/repository.go
```

Handler rules:

- Parse JSON with `json.NewDecoder`.
- Validate request using `validator`.
- Read auth claims with `middleware.GetClaims`.
- Do not write SQL.
- Do not own business rules.

Service rules:

- Own business rules.
- Call repository for durable state.
- Call Redis for hot state.
- Publish analytics and WebSocket events.
- Return typed errors.

Repository rules:

- Own SQL strings.
- Convert database rows to structs.
- Return domain structs.
- Convert `pgx.ErrNoRows` into domain not-found errors when appropriate.

## Adding a New Endpoint

Checklist:

1. Add handler method.
2. Add service method.
3. Add repository method if SQL is required.
4. Register route in `cmd/server/main.go`.
5. Protect with auth and role middleware.
6. Add/update Swagger path.
7. Add unit/integration tests.
8. Update docs if flow changes.

## Endpoint Naming

Use nouns and actions consistently:

```text
/api/v1/customer/rides
/api/v1/driver/rides/{ride_id}/accept
/api/v1/driver/rides/{ride_id}/negotiation/lock-fare
/api/v1/admin/drivers/{id}/approve
```

## WebSocket Messages

Shape:

```json
{
  "type": "ride_confirmed",
  "ride_id": "ride-id",
  "payload": {
    "agreed_fare": 2000
  }
}
```

Current important message types:

- `ride_request`
- `negotiation_message`
- `ride_confirmed`
- `driver_arrived`
- `ride_pickup_expired`
- `ride_cancelled`
- `ride_completed`

## Redis Key Rule

Do not create ad hoc Redis key strings in feature code. Add or reuse key builders in `pkg/redis`.

## Database Migration Rule

For every schema change:

1. Add `NNN_description.up.sql`.
2. Add `NNN_description.down.sql`.
3. Make migrations idempotent where possible.
4. Update `docs/architecture/DATA_MODEL.md`.
5. Add a migration test when the change is important to business flow.

## Swagger Rule

Swagger must reflect public API behavior:

- Do not document fare suggestions until the product exposes them.
- Include auth security on protected routes.
- Keep v1 routes under `/api/v1`.
- Add examples for request bodies.

## Production Coding Practices

- Keep side effects obvious in service methods.
- Prefer typed errors over raw errors.
- Keep state transitions centralized in `ride/statemachine.go`.
- Avoid business logic in handlers.
- Avoid SQL in services.
- Avoid Redis key literals outside `pkg/redis`.
- Add tests for every business rule.
