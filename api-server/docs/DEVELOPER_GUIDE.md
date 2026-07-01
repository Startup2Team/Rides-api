# Rides Backend — Developer Guide

Everything you need to work on this codebase: structure, patterns, how to write code, how to test it, and how to ship it.

---

## Table of Contents

1. [Project Structure](#1-project-structure)
2. [Module Pattern](#2-module-pattern)
3. [Adding a New Module](#3-adding-a-new-module)
4. [Error Handling](#4-error-handling)
5. [HTTP Responses](#5-http-responses)
6. [Request Validation](#6-request-validation)
7. [Redis Keys](#7-redis-keys)
8. [Database & Migrations](#8-database--migrations)
9. [Writing Tests](#9-writing-tests)
10. [Branch Strategy & Git Workflow](#10-branch-strategy--git-workflow)
11. [Commit Messages](#11-commit-messages)
12. [Pull Request Rules](#12-pull-request-rules)
13. [Local Development](#13-local-development)

---

## 1. Project Structure

```
api-server/
├── cmd/server/main.go          # Entry point — wires everything together
├── config/
│   ├── config.go               # All env vars in one place
│   └── openapi.json            # API spec
├── internal/                   # Business logic — not importable from outside
│   ├── auth/                   # Authentication (OTP, JWT)
│   ├── ride/                   # Ride lifecycle + state machine
│   ├── driver/                 # Driver profile, location, availability
│   ├── customer/               # Customer profile
│   ├── negotiation/            # Fare negotiation rounds
│   ├── matching/               # Driver search + scoring engine
│   ├── location/               # Route cache, landmarks, saved locations
│   ├── tracking/               # WebSocket real-time GPS hub
│   ├── analytics/              # Event stream + aggregation
│   ├── admin/                  # Admin dashboard actions
│   ├── notification/           # FCM push notifications
│   ├── payment/                # MoMo payment integration
│   ├── telephony/              # SMS OTP (AfricasTalking)
│   └── middleware/             # Auth, rate limit, role checks
├── pkg/                        # Shared utilities — safe to import anywhere
│   ├── errors/                 # AppError type + all sentinel errors
│   ├── respond/                # JSON response helpers
│   ├── geo/                    # Geospatial math, geohash
│   ├── redis/                  # Redis client + ALL key definitions
│   ├── postgres/               # PGX connection pool
│   └── logger/                 # Zerolog wrapper
├── migrations/                 # SQL files, numbered sequentially
├── test/
│   ├── integration/            # HTTP-level tests (no real DB)
│   └── e2e/                    # End-to-end flow tests
├── nginx/                      # Nginx config for production
├── docs/                       # You are here
├── Dockerfile
├── docker-compose.yml          # Local dev stack
├── docker-compose.prod.yml     # Production stack
└── Makefile                    # All common commands
```

**Rule:** business logic lives in `internal/`. Shared utilities live in `pkg/`. Nothing in `pkg/` imports from `internal/`.

---

## 2. Module Pattern

Every domain module has the same three-file structure. No exceptions.

```
internal/<module>/
├── handler.go      # HTTP layer — parse, validate, call service, respond
├── service.go      # Business logic — orchestrate repo, Redis, other services
├── repository.go   # Database queries only — no business logic here
└── types.go        # Domain structs (if the module has them)
```

### What goes where

| Layer | Responsibility | Must NOT |
|---|---|---|
| `handler.go` | Parse JSON, validate input, call service, write response | Contain business logic or DB queries |
| `service.go` | Business rules, Redis, calling other services, error decisions | Write HTTP responses or raw SQL |
| `repository.go` | SQL queries via pgx, row scanning | Contain any business rules |

### Dependency injection

Every constructor receives its dependencies. No global variables.

```go
// repository.go
type Repository struct {
    db *pgxpool.Pool
}
func NewRepository(db *pgxpool.Pool) *Repository {
    return &Repository{db: db}
}

// service.go
type Service struct {
    repo   *Repository
    redis  *goredis.Client
    notify *notification.Service
    cfg    *config.Config
    log    zerolog.Logger
}
func NewService(repo *Repository, rdb *goredis.Client, notify *notification.Service, cfg *config.Config, log zerolog.Logger) *Service {
    return &Service{repo: repo, redis: rdb, notify: notify, cfg: cfg, log: log}
}

// handler.go
type Handler struct {
    svc *Service
}
func NewHandler(svc *Service) *Handler {
    return &Handler{svc: svc}
}
```

### Circular dependencies

If module A needs module B and module B needs module A, break it with an interface and a setter:

```go
// In ride/service.go — define the interface
type MatchingEngineInterface interface {
    StartSearch(rideID string, pickup geo.Point, vehicleType string)
}

// Add a setter instead of constructor injection
func (s *Service) SetMatchingEngine(engine MatchingEngineInterface) {
    s.engine = engine
}
```

Then in `main.go`, set it after both are constructed.

---

## 3. Adding a New Module

Follow these exact steps. Take `packages` as an example.

**Step 1 — Create the folder and files**
```
internal/packages/
├── handler.go
├── service.go
├── repository.go
└── types.go
```

**Step 2 — Write types.go first**
Define your domain structs before writing any logic.

**Step 3 — Write repository.go**
Only SQL. Scan rows into your types. Return `*AppError` sentinels on known DB errors:

```go
func (r *Repository) FindByID(ctx context.Context, id string) (*Package, error) {
    p := &Package{}
    err := r.db.QueryRow(ctx, `SELECT id, name FROM ride_packages WHERE id = $1`, id).
        Scan(&p.ID, &p.Name)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, apperrors.ErrNotFound
        }
        return nil, err
    }
    return p, nil
}
```

**Step 4 — Write service.go**
Business rules here. Log meaningful events. Return `*AppError` for known failures:

```go
func (s *Service) BuyPackage(ctx context.Context, driverID, packageID string) (*DriverCredit, error) {
    pkg, err := s.repo.FindByID(ctx, packageID)
    if err != nil {
        return nil, err
    }
    if !pkg.IsActive {
        return nil, apperrors.New(http.StatusGone, "PACKAGE_UNAVAILABLE", "this package is no longer available")
    }
    // ... payment, credit creation ...
    s.log.Info().Str("driver_id", driverID).Str("package_id", packageID).Msg("package purchased")
    return credit, nil
}
```

**Step 5 — Write handler.go**
Parse → validate → call service → respond. Nothing else.

```go
func (h *Handler) PurchasePackage(w http.ResponseWriter, r *http.Request) {
    claims := middleware.GetClaims(r)

    var body struct {
        PackageID string `json:"package_id" validate:"required,uuid"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        respond.Error(w, apperrors.ErrBadRequest)
        return
    }
    if err := validate.Struct(body); err != nil {
        respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
        return
    }

    credit, err := h.svc.BuyPackage(r.Context(), claims.UserID, body.PackageID)
    if err != nil {
        respond.Error(w, err)
        return
    }
    respond.Created(w, credit)
}
```

**Step 6 — Wire in main.go**

```go
// Repositories
pkgRepo := packages.NewRepository(db)

// Services
pkgSvc := packages.NewService(pkgRepo, paymentSvc, log)

// Handlers
pkgH := packages.NewHandler(pkgSvc)

// Routes (inside the driver group)
r.Get("/packages",           pkgH.ListPackages)
r.Post("/packages/purchase", pkgH.PurchasePackage)
r.Get("/credits",            pkgH.GetCredits)
```

**Step 7 — Add migrations** (see [Database & Migrations](#8-database--migrations))

---

## 4. Error Handling

### Sentinel errors

Pre-defined errors live in `pkg/errors/errors.go`. Use them for known, expected failures:

```go
// Return sentinel directly
return nil, apperrors.ErrNotFound
return nil, apperrors.ErrUnauthorized

// Check against sentinel
if errors.Is(err, apperrors.ErrNotFound) { ... }
```

### Dynamic errors

For errors with context-specific messages, use `New` or `Newf`:

```go
return nil, apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "no ride credits remaining — buy a package to continue")

return nil, apperrors.Newf(http.StatusConflict, "RIDE_ACTIVE", "you already have an active ride: %s", rideID)
```

### In handlers — always use respond.Error

```go
if err != nil {
    respond.Error(w, err) // Handles AppError or falls back to 500
    return
}
```

**Never** return raw `errors.New()` or `fmt.Errorf()` from service/repository if the handler is going to send it to the client — the client will get a 500. Wrap it in AppError if it's a known failure.

---

## 5. HTTP Responses

All responses use the envelope format:

```json
// Success
{ "data": { ... } }

// Error  
{ "error": { "code": "RIDE_NOT_FOUND", "message": "ride not found" } }
```

### Response helpers

```go
respond.OK(w, data)           // 200 with data
respond.Created(w, data)      // 201 with data
respond.NoContent(w)          // 204 no body
respond.Error(w, err)         // uses AppError status code
respond.ErrorMsg(w, 400, "VALIDATION", "phone_number required")
```

### Status code guide

| Situation | Code |
|---|---|
| Successful fetch | 200 |
| Resource created | 201 |
| Action with no return value | 204 |
| Invalid input / missing fields | 400 |
| Not authenticated | 401 |
| Authenticated but not allowed | 403 |
| Resource not found | 404 |
| Conflict (duplicate, wrong state) | 409 |
| Business rule violation | 422 |
| Payment required / no credits | 402 |
| Rate limited | 429 |
| Unexpected server error | 500 |

---

## 6. Request Validation

Use `go-playground/validator` with struct tags. Declare one `validate` instance per package:

```go
var validate = validator.New()

var body struct {
    Phone  string `json:"phone_number" validate:"required,e164"`
    Name   string `json:"full_name"    validate:"required,min=2,max=100"`
    Role   string `json:"role"         validate:"required,oneof=customer driver"`
    Amount int    `json:"amount_rwf"   validate:"required,min=100"`
}

if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
    respond.Error(w, apperrors.ErrBadRequest)
    return
}
if err := validate.Struct(body); err != nil {
    respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
    return
}
```

**Always decode first, then validate.** Never trust the client — validate every field that matters.

---

## 7. Redis Keys

**All Redis keys are defined in `pkg/redis/redis.go`.** Never write a key string inline anywhere else. Add new keys there and use `rkeys.K.YourKey(...)`.

```go
// WRONG — never do this
rdb.Set(ctx, fmt.Sprintf("driver:%s:state", driverID), ...)

// CORRECT
rdb.Set(ctx, rkeys.K.DriverState(driverID), ...)
```

### Adding a new key

```go
// In pkg/redis/redis.go, inside the Keys type:
func (Keys) DriverRideCredits(driverID string) string {
    return fmt.Sprintf("driver:%s:credits_remaining", driverID)
}
```

### Key naming convention

```
<entity>:<id>:<attribute>
Examples:
  driver:abc123:state
  ride:xyz789:state
  customer:abc123:active_ride
  ratelimit:otp:+250780000000
```

---

## 8. Database & Migrations

### Migration naming

Files are numbered sequentially. Never change existing migrations — always add a new one.

```
017_create_vehicle_types.up.sql
017_create_vehicle_types.down.sql
018_create_ride_packages.up.sql
018_create_ride_packages.down.sql
```

### Writing a migration

```sql
-- 017_create_vehicle_types.up.sql
CREATE TABLE vehicle_types (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  code            VARCHAR(20) UNIQUE NOT NULL,
  display_name    VARCHAR(50) NOT NULL,
  credit_cost_rwf INTEGER NOT NULL,
  is_active       BOOLEAN DEFAULT true,
  created_at      TIMESTAMPTZ DEFAULT now()
);

-- Always seed static/reference data in the same migration
INSERT INTO vehicle_types (code, display_name, credit_cost_rwf) VALUES
  ('MOTO_BIKE', 'Moto', 30),
  ('CAB',       'Cab',  200),
  ('PICKUP',    'Pickup', 100);
```

```sql
-- 017_create_vehicle_types.down.sql
DROP TABLE IF EXISTS vehicle_types;
```

### Repository query patterns

```go
// Single row
var t VehicleType
err := r.db.QueryRow(ctx, `SELECT id, code FROM vehicle_types WHERE id = $1`, id).
    Scan(&t.ID, &t.Code)
if errors.Is(err, pgx.ErrNoRows) {
    return nil, apperrors.ErrNotFound
}

// Multiple rows
rows, err := r.db.Query(ctx, `SELECT id, code FROM vehicle_types WHERE is_active = true`)
defer rows.Close()
for rows.Next() {
    var t VehicleType
    rows.Scan(&t.ID, &t.Code)
    results = append(results, &t)
}

// Geospatial — extract coords from PostGIS geometry
`SELECT id, ST_X(location::geometry) AS lng, ST_Y(location::geometry) AS lat FROM driver_locations`
```

Use `$1, $2, $3` placeholders — never string-concatenate SQL.

---

## 9. Writing Tests

### Unit tests — co-located, same package with `_test` suffix

Put them next to the file they test:

```
internal/packages/
├── service.go
├── service_test.go     ← unit test for service
├── repository.go
└── handler.go
```

Use table-driven tests:

```go
package packages_test

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/workspace/ride-platform/internal/packages"
)

func TestCheckAndDeductCredit(t *testing.T) {
    tests := []struct {
        name      string
        credits   int
        wantErr   bool
        wantCode  string
    }{
        {"has credits",    5, false, ""},
        {"no credits",     0, true,  "NO_CREDITS"},
        {"expired credit", -1, true, "NO_CREDITS"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Use a mock repo, not a real DB
            svc := packages.NewService(mockRepo(tt.credits), nil, testLogger())
            err := svc.CheckAndDeductCredit(ctx, "driver-1", "MOTO_BIKE")
            if tt.wantErr {
                assert.ErrorIs(t, err, packages.ErrNoCredits)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

### Integration tests — `test/integration/`

Test HTTP handler behaviour without a real DB. Use `httptest` and mock handlers:

```go
package integration_test

func TestRideCreate_Returns201(t *testing.T) {
    r := chi.NewRouter()
    r.Post("/rides", func(w http.ResponseWriter, r *http.Request) {
        respond.Created(w, map[string]string{"ride_id": "test-uuid", "status": "SEARCHING"})
    })
    // ... assert status code and response body
}
```

### E2E tests — `test/e2e/`

Test full flows using miniredis for Redis-backed logic:

```go
package e2e_test

func TestHappyPathStateProgression(t *testing.T) {
    for _, step := range happyPathSteps {
        err := ride.ValidateTransition(step.from, step.to)
        assert.NoError(t, err)
    }
}
```

### Rules

- No test should connect to a real database
- Use miniredis (`github.com/alicebob/miniredis/v2`) for Redis tests
- Use `t.Cleanup(func() { ... })` instead of `defer` in test helpers
- Run with the race detector: `go test -race ./...`

---

## 10. Branch Strategy & Git Workflow

```
feature/your-feature-name
        ↓  PR → CI must pass + 1 reviewer
       dev   (staging — Railway auto-deploys)
        ↓  PR → CI must pass + 1 reviewer
      main   (production — Railway auto-deploys)
```

### The rules

- **Never push directly to `dev` or `main`.** Both are protected.
- All work starts from `dev`, not `main`.
- PRs to `dev` require: CI passing + 1 approval.
- PRs to `main` require: CI passing + 1 approval. Admins cannot bypass.
- `main` should only ever receive PRs from `dev`.

### Day-to-day workflow

```bash
# 1. Start new work — always branch from dev
git checkout dev
git pull origin dev
git checkout -b feature/driver-packages

# 2. Work, commit often
git add api-server/internal/packages/
git commit -m "feat(packages): add repository and service layer"

# 3. Keep your branch up to date with dev
git fetch origin
git rebase origin/dev

# 4. Push and open PR to dev
git push origin feature/driver-packages
# Then open PR on GitHub: base = dev, compare = feature/driver-packages

# 5. After PR is merged to dev and staging looks good
# Open a second PR: base = main, compare = dev
```

### CI checks that must pass

Every PR runs three checks automatically:

| Check | What it verifies |
|---|---|
| **Lint** | `go vet` passes + code is formatted (`gofmt`) |
| **Test** | All tests pass with race detector enabled |
| **Docker Build** | The Dockerfile compiles successfully |

If any of these fail, the PR cannot be merged.

---

## 11. Commit Messages

Format: `type(scope): short description`

| Type | Use for |
|---|---|
| `feat` | New feature or endpoint |
| `fix` | Bug fix |
| `refactor` | Code restructure, no behaviour change |
| `test` | Adding or fixing tests |
| `migration` | New SQL migration |
| `devops` | CI, Docker, deployment config |
| `docs` | Documentation only |
| `chore` | Dependency updates, config tweaks |

**Examples:**

```
feat(packages): add credit check before ride acceptance
fix(ride): prevent double credit deduction on retry
migration(017): add vehicle_types table with seed data
refactor(customer): move profile logic from handler to service
test(matching): add table-driven tests for scoring algorithm
```

**Rules:**
- Lowercase, no period at the end
- Scope is the module name: `auth`, `ride`, `driver`, `packages`, etc.
- Description is what it does, not what you did: "add X" not "added X"
- Keep the subject line under 72 characters
- If it needs more explanation, add a blank line then a paragraph body

---

## 12. Pull Request Rules

### What a good PR looks like

- **One thing at a time.** A PR that adds a feature and refactors something else will be asked to split.
- **Small is better.** Aim for under 400 lines changed. Reviewers skip reading large PRs.
- **Tests included.** No feature PR merges without at least one test covering the happy path.
- **Migration included.** If you added a table, the migration file must be in the same PR.

### PR title format

Same as commit messages: `feat(packages): add driver credit purchase endpoint`

### Checklist before opening a PR

```
[ ] go build ./... passes locally
[ ] go test -race ./... passes locally
[ ] gofmt -l . returns nothing (run make fmt)
[ ] New keys added to pkg/redis/redis.go (not inline)
[ ] New errors added to pkg/errors/errors.go (not inline)
[ ] Migration has a matching .down.sql file
[ ] No .env or secret values committed
```

---

## 13. Local Development

### First time setup

```bash
cd api-server
make env          # copies .env.example → .env
make deps         # downloads Go modules
make db-up        # starts Postgres, Redis, MinIO in Docker
make dev          # runs the API on :8080
```

### Common commands

```bash
make test          # run all tests
make fmt           # format all Go files
make coverage      # tests + coverage report
make swagger-check # validate openapi.json
make smoke         # hit /health and /swagger endpoints
make docker-up     # run full stack (API + DB) in Docker
make docker-down   # stop everything
```

### Environment variables

Copy `.env.example` to `.env` and fill in the required values. Required ones:

```
DATABASE_URL        postgres://ride:ride_secret@localhost:5432/rideplatform
REDIS_URL           redis://localhost:6379
JWT_ACCESS_SECRET   any-long-random-string
JWT_REFRESH_SECRET  different-long-random-string
```

All others have safe defaults for local development.

### Running tests

```bash
go test ./...                    # all tests
go test -race ./...              # with race detector (use this before pushing)
go test ./internal/ride/...      # single module
go test -run TestStateMachine    # single test by name
go test -v ./test/e2e/...        # e2e tests verbose
```

### 14. Proxy Upload Authentication

In production, proxy PUT uploads (`PUT /api/v1/uploads/objects/*`) require a short-lived signature token.
This token is automatically appended to the `upload_url` returned by `POST /api/v1/uploads/presigned-url`.

Clients must either:
- Use the full `upload_url` containing the `?token=` query parameter.
- Send the token in the `X-Upload-Token` header.
