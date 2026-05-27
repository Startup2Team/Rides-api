# Test Strategy

## Current State

The project has:

- Unit tests for ride state machine.
- Unit tests for negotiation rules.
- Unit tests for matching lock/scoring pieces.
- Unit tests for shared packages.
- Smoke-style endpoint tests.
- Lightweight end-to-end flow documentation tests.

Current total coverage is low and should be improved before a serious production rollout.

## Test Levels

### Unit Tests

Use for:

- Pure functions.
- State machine rules.
- Payout calculation.
- Redis key builders.
- Geo helpers.
- Response helpers.
- Validation edge cases.

Command:

```bash
go test ./...
```

### Integration Tests

Use for:

- Repository SQL.
- Migrations.
- PostGIS geofence checks.
- Redis state transitions.
- Route cache updates.
- Full customer-driver lifecycle.

Preferred approach:

- Use Docker Compose or testcontainers.
- Start Postgres/PostGIS and Redis.
- Run migrations.
- Seed test users/drivers.
- Exercise real HTTP endpoints.

### Contract Tests

Use for:

- Swagger validity.
- Required route paths.
- Response envelope shape.
- Auth/role protection behavior.

### Manual MVP QA

Before a deploy, manually verify:

1. Register customer and driver.
2. Approve driver as admin.
3. Driver accepts policy.
4. Driver goes online and sends location.
5. Customer creates ride.
6. Driver receives request and accepts.
7. Fare is negotiated or manually locked.
8. Driver goes en-route.
9. Driver arrives.
10. Driver starts ride.
11. Driver completes ride.
12. Customer sees ride history.
13. Driver sees payout earnings.

## Coverage Targets

| Stage | Target |
|---|---:|
| Current MVP | Raise core business packages above 30%. |
| Pre-beta | 50% overall with critical flows covered. |
| Production | 70%+ overall and integration tests for all major flows. |

Critical packages:

- `internal/ride`
- `internal/matching`
- `internal/negotiation`
- `internal/driver`
- `internal/auth`
- `internal/admin`
- `internal/location`
- `internal/middleware`

## Required Tests For New Features

Every backend feature should include at least one of:

- Handler validation test.
- Service business-rule test.
- Repository integration test.
- Swagger regression test.
- State transition test.

## Commands

```bash
make test
make coverage
make swagger-check
make smoke
```

## Test Data Guidelines

- Use fixed UUID-like strings only in unit tests that do not hit DB.
- Use real UUIDs for database integration tests.
- Avoid depending on wall-clock timing except for small timer-specific tests.
- Keep Redis tests isolated with `miniredis` where possible.

## Known Gaps

- No full HTTP integration suite against real Postgres and Redis yet.
- Admin service lacks focused tests.
- Auth repository/service needs stronger tests.
- WebSocket behavior needs integration tests.
- Route fare aggregation should be verified against Postgres in an integration test.
