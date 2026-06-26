# Backend Testing Guide — Rides API

**Assignee:** @salomonPr  
**Branch:** always pull from `dev` before starting  
**Goal:** Cover every endpoint with unit + integration tests so CI blocks any broken PR automatically

---

## 0. TL;DR Quick Start

```bash
git checkout dev && git pull origin dev
cd api-server

# Run all existing tests (must stay green)
go test -race -count=1 ./...

# Run only integration tests
go test -race ./test/integration/...

# Run only e2e tests
go test -race ./test/e2e/...

# Run a single package
go test -race ./internal/admin/...
```

---

## 1. Repo Structure

```
api-server/
├── cmd/server/main.go           ← All routes registered here
├── internal/
│   ├── admin/                   ← Admin business logic + handler
│   ├── auth/                    ← OTP, JWT, refresh
│   ├── customer/                ← Customer profile, ride ops
│   ├── driver/                  ← Driver profile, location, matching
│   ├── fare/                    ← Pricing engine + config
│   ├── location/                ← Route cache, landmarks, suggestions
│   ├── matching/                ← Driver dispatch engine
│   ├── negotiation/             ← Fare negotiation rounds
│   ├── packages/                ← Driver ride credits
│   ├── ride/                    ← Ride lifecycle state machine
│   ├── team/                    ← Admin team member management + 2FA
│   ├── tracking/                ← WebSocket hub, driver/customer WS
│   ├── incidents/               ← Incident CRUD
│   ├── tickets/                 ← Support ticket CRUD
│   ├── inbox/                   ← Admin inbox
│   ├── reports/                 ← Report generation
│   ├── settings/                ← Platform settings
│   └── dashboard/               ← Dashboard KPI aggregation
├── pkg/
│   ├── respond/                 ← Unified JSON response helpers
│   ├── errors/                  ← App error types
│   ├── geo/                     ← Haversine, PostGIS helpers
│   └── redis/                   ← Redis key builders
└── test/
    ├── integration/             ← HTTP handler tests (httptest, no DB)
    └── e2e/                     ← Full flow tests (miniredis, no DB)
```

---

## 2. Testing Philosophy

### Two test layers — pick the right one

| Layer | Where | Uses DB? | Uses Redis? | Purpose |
|---|---|---|---|---|
| **Unit** | `internal/<pkg>/<file>_test.go` | No | No (miniredis OK) | Pure logic: state machines, fare calc, geohash, validation |
| **Integration** | `test/integration/` | No | No | HTTP handler in/out: correct status codes, JSON shape, auth checks |
| **E2E** | `test/e2e/` | No | miniredis | Multi-step flows: ride lifecycle, negotiation rounds |

> **No real Postgres in tests.** Use `httptest.NewServer`, mock repositories with interfaces, and `miniredis` for Redis-dependent logic. CI has no database service.

### The three things every test must check

1. **Happy path** — correct input → correct 2xx response + correct JSON shape
2. **Auth guard** — missing/invalid token → `401 UNAUTHORIZED`
3. **Validation** — malformed/missing required fields → `400 VALIDATION`

For state-mutating endpoints also check:
4. **Not found** — invalid UUID → `404 NOT_FOUND`
5. **Conflict** — duplicate action (e.g. approve already-approved driver) → correct error

---

## 3. Test Patterns Already Established (copy these)

### 3.1 Handler test with httptest (no DB needed)

```go
package integration_test

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/go-chi/chi/v5"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/workspace/ride-platform/pkg/respond"
)

func TestHealthCheck(t *testing.T) {
    r := chi.NewRouter()
    r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
        respond.OK(w, map[string]string{"status": "ok"})
    })

    req := httptest.NewRequest(http.MethodGet, "/health", nil)
    rr := httptest.NewRecorder()
    r.ServeHTTP(rr, req)

    assert.Equal(t, http.StatusOK, rr.Code)
    var body struct {
        Data struct {
            Status string `json:"status"`
        } `json:"data"`
    }
    require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
    assert.Equal(t, "ok", body.Data.Status)
}
```

### 3.2 Authenticated handler test

Use `makeJWT()` from `test/integration/endpoints_test.go` — it's already there:

```go
func TestAdminEndpoint_RequiresAuth(t *testing.T) {
    r := chi.NewRouter()
    r.Use(mw.Authenticate(testCfg, testRDB))
    r.Get("/admin/drivers", adminH.ListDrivers)

    // ── No token → 401 ──────────────────────────────────────────────────
    req := httptest.NewRequest(http.MethodGet, "/admin/drivers", nil)
    rr := httptest.NewRecorder()
    r.ServeHTTP(rr, req)
    assert.Equal(t, http.StatusUnauthorized, rr.Code)

    // ── Valid admin token → 200 ──────────────────────────────────────────
    token := makeJWT(t, os.Getenv("JWT_SECRET"), "some-uuid", "ADMIN", "access", time.Hour)
    req2 := httptest.NewRequest(http.MethodGet, "/admin/drivers", nil)
    req2.Header.Set("Authorization", "Bearer "+token)
    rr2 := httptest.NewRecorder()
    r.ServeHTTP(rr2, req2)
    assert.Equal(t, http.StatusOK, rr2.Code)
}
```

### 3.3 Mock repository (interface-based)

Every handler accepts an interface, not a concrete type. Write a minimal mock:

```go
type mockAdminService struct {
    drivers []map[string]interface{}
    err     error
}

func (m *mockAdminService) ListDrivers(ctx context.Context, status string, limit, offset int) ([]map[string]interface{}, error) {
    return m.drivers, m.err
}
```

### 3.4 Miniredis for Redis-dependent logic

```go
func newTestRedis(t *testing.T) *goredis.Client {
    t.Helper()
    mr, err := miniredis.Run()
    require.NoError(t, err)
    t.Cleanup(mr.Close)
    client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
    t.Cleanup(func() { client.Close() })
    return client
}
```

---

## 4. JSON Response Envelope

Every response wraps data in `{"data": ...}` or `{"error": {"code": "...", "message": "..."}}`.

Use `decodeData()` (already in `test/integration/endpoints_test.go`):

```go
var result struct {
    Drivers []map[string]interface{} `json:"drivers"`
}
decodeData(t, rr, &result)
assert.Len(t, result.Drivers, 3)
```

Error shape:
```json
{
  "error": {
    "code": "VALIDATION",
    "message": "transport_type is required"
  }
}
```

---

## 5. Complete Endpoint Test Checklist

Work through each group. Create one `_test.go` file per package.

---

### GROUP A — Public & Health

**File:** `test/integration/public_test.go`

| Endpoint | Test cases |
|---|---|
| `GET /health` | 200, body `{"status":"ok"}` |
| `GET /api/v1/pricing` | 200, returns array of vehicle types |
| `GET /swagger` | 200 |

---

### GROUP B — Auth  (`internal/auth/`)

**File:** `internal/auth/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `POST /api/v1/auth/register` | ✅ new phone → 200 + `dev_otp` in dev mode |
| | ❌ missing phone → 400 |
| | ❌ invalid phone format → 400 |
| `POST /api/v1/auth/verify-otp` | ✅ valid OTP → 200 + `access_token` + `refresh_token` |
| | ❌ wrong OTP → 401 |
| | ❌ expired OTP → 401 |
| | ❌ already-used OTP → 401 |
| `POST /api/v1/auth/refresh` | ✅ valid refresh token → 200 + new `access_token` |
| | ❌ expired refresh token → 401 |
| | ❌ access token used as refresh → 401 |
| `POST /api/v1/auth/logout` | ✅ valid token → 204 |
| | ❌ no token → 401 |

---

### GROUP C — Customer  (`internal/customer/`)

**File:** `internal/customer/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `GET /api/v1/customer/profile` | ✅ valid token → 200 + profile fields |
| | ❌ no token → 401 |
| `PUT /api/v1/customer/profile` | ✅ valid update → 200 |
| | ❌ no token → 401 |
| `POST /api/v1/customer/location` | ✅ Kigali coords + MOTO_BIKE → 200 + `drivers` array |
| | ❌ missing `transport_type` → 400 |
| | ❌ out-of-range lat/lng → 400 |
| | ❌ no token → 401 |
| `GET /api/v1/customer/fare-estimate` | ✅ valid Kigali pickup+dest → 200 + `breakdown.total_fare_rwf` > 0 |
| | ❌ missing `transport_type` → 400 |
| | ❌ missing `pickup_lat` → 400 |
| | ❌ no token → 401 |
| `POST /api/v1/customer/rides` | ✅ valid request body → 201 + ride `id` + `status: SEARCHING` |
| | ❌ missing `transport_type` → 400 |
| | ❌ identical pickup and destination → 400 |
| | ❌ no token → 401 |
| `GET /api/v1/customer/rides` | ✅ returns paginated rides array |
| | ❌ no token → 401 |
| `GET /api/v1/customer/rides/{ride_id}` | ✅ own ride → 200 |
| | ❌ non-existent ride_id → 404 |
| | ❌ someone else's ride_id → 403 or 404 |
| `DELETE /api/v1/customer/rides/{ride_id}` | ✅ SEARCHING ride → 200 cancelled |
| | ❌ IN_PROGRESS ride → 409 (cannot cancel in-progress) |
| | ❌ no token → 401 |

---

### GROUP D — Driver  (`internal/driver/`)

**File:** `internal/driver/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `POST /api/v1/driver/apply` | ✅ valid body → 201 + profile `approval_status: PENDING_REVIEW` |
| | ❌ duplicate application → 409 |
| | ❌ missing required fields → 400 |
| `GET /api/v1/driver/profile` | ✅ returns profile for authenticated driver |
| | ❌ no profile yet → 404 |
| `POST /api/v1/driver/policy/accept` | ✅ sets `policy_accepted: true` |
| `POST /api/v1/driver/documents` | ✅ valid upload → 201 |
| | ❌ unsupported document type → 400 |
| `POST /api/v1/driver/availability` | ✅ `{"is_online": true}` on APPROVED driver → 200 |
| | ❌ PENDING driver going online → 403 |
| `POST /api/v1/driver/location` | ✅ valid Kigali coords → 200 |
| | ❌ speed > 200 km/h → GPS anomaly logged (check side effect) |
| `GET /api/v1/driver/packages` | ✅ returns available packages for driver's transport type |
| `GET /api/v1/driver/credits` | ✅ returns active credits |
| `POST /api/v1/driver/rides/{ride_id}/accept` | ✅ driver has credits + ride SEARCHING → 200 |
| | ❌ no credits → 402 PAYMENT_REQUIRED |
| | ❌ ride already accepted → 409 |
| `POST /api/v1/driver/rides/{ride_id}/decline` | ✅ decrements acceptance_rate |
| `POST /api/v1/driver/rides/{ride_id}/en-route` | ✅ CONFIRMED ride → 200 status DRIVER_EN_ROUTE |
| `POST /api/v1/driver/rides/{ride_id}/arrive` | ✅ DRIVER_EN_ROUTE → DRIVER_ARRIVED |
| `POST /api/v1/driver/rides/{ride_id}/start` | ✅ DRIVER_ARRIVED → IN_PROGRESS |
| `POST /api/v1/driver/rides/{ride_id}/complete` | ✅ IN_PROGRESS → COMPLETED + credit deducted |
| `GET /api/v1/driver/earnings/daily` | ✅ returns `total_rwf` number |
| `GET /api/v1/driver/stats` | ✅ returns `total_rides`, `acceptance_rate`, `completion_rate` |

---

### GROUP E — Negotiation  (`internal/negotiation/`)

**File:** `internal/negotiation/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `POST /customer/rides/{id}/negotiation/propose` | ✅ round 1 customer propose → 200 |
| | ❌ more than 3 rounds → 422 |
| | ❌ fare below 0 → 400 |
| `POST /driver/rides/{id}/negotiation/propose` | ✅ driver counter-propose → 200 |
| `POST .../negotiation/accept` | ✅ acceptance locks `agreed_fare_rwf` on ride |
| `POST .../negotiation/decline` | ✅ decline increments round counter |
| `POST .../negotiation/lock-fare` (driver) | ✅ fare locked, ride moves to CONFIRMED |
| `POST .../negotiation/initiate-call` (driver) | ✅ only allowed on round 3 |
| | ❌ before round 3 → 422 |

---

### GROUP F — Admin Auth  (`internal/team/`)

**File:** `internal/team/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `POST /api/v1/admin/auth/login` | ✅ valid email+password → 200 + `requires_2fa: true` or token |
| | ❌ wrong password → 401 |
| | ❌ unknown email → 401 |
| `POST /api/v1/admin/auth/2fa/verify` | ✅ valid TOTP code → 200 + admin JWT |
| | ❌ wrong code → 401 |
| `POST /api/v1/admin/auth/2fa/backup` | ✅ valid backup code → 200 + admin JWT |
| | ❌ already-used backup code → 401 |
| `POST /api/v1/admin/auth/logout` | ✅ invalidates session in Redis |
| `POST /api/v1/admin/auth/totp/reset` | ✅ valid current TOTP → new QR + secret |
| | ❌ wrong TOTP → 401 |
| `GET /api/v1/admin/account/2fa/setup` | ✅ returns QR URL + secret |
| `POST /api/v1/admin/account/2fa/enable` | ✅ enables 2FA after first TOTP verify |
| `POST /api/v1/admin/account/2fa/disable` | ✅ disables with valid TOTP |

---

### GROUP G — Admin Drivers  (`internal/admin/`)

**File:** `internal/admin/handler_test.go`  

> These are the most important — driver lifecycle is the core business flow.

| Endpoint | Test cases |
|---|---|
| `GET /api/v1/admin/drivers` | ✅ returns paginated list |
| | ✅ `?status=PENDING_REVIEW` filters correctly |
| | ❌ no admin token → 401 |
| `GET /api/v1/admin/drivers/overview` | ✅ returns counts by status |
| `GET /api/v1/admin/drivers/{id}` | ✅ returns full driver + documents |
| | ❌ unknown id → 404 |
| `POST /api/v1/admin/drivers/{id}/approve` | ✅ PENDING_REVIEW → APPROVED + user `role_state: DRIVER_ACTIVE` |
| | ❌ admin approving own profile (self-approval) → 403 |
| | ❌ already APPROVED → 409 or no-op |
| `POST /api/v1/admin/drivers/{id}/reject` | ✅ sets REJECTED + `rejection_reason` |
| | ❌ missing reason → 400 |
| `POST /api/v1/admin/drivers/{id}/suspend` | ✅ APPROVED → SUSPENDED + `is_suspended: true` on user |
| | ✅ `suspension_until` is set correctly (now + duration) |
| | ❌ missing `duration_hours` → 400 |
| `POST /api/v1/admin/drivers/{id}/reinstate` | ✅ SUSPENDED → APPROVED + `is_suspended: false` |
| `PATCH /api/v1/admin/drivers/{id}/verify` | ✅ sets `verified: true` on documents |
| `PATCH /api/v1/admin/drivers/{id}/status` | ✅ valid status transition → 200 |
| | ❌ invalid status value → 400 |

---

### GROUP H — Admin Customers

**File:** `internal/admin/handler_test.go` (same file, different test functions)

| Endpoint | Test cases |
|---|---|
| `GET /api/v1/admin/customers` | ✅ paginated list |
| `GET /api/v1/admin/customers/{id}` | ✅ returns profile + ride history |
| `PATCH /api/v1/admin/customers/{id}/ban` | ✅ sets `is_suspended: true` permanently |
| `POST /api/v1/admin/customers/{id}/suspend` | ✅ temporary suspension with `duration_hours` |
| `POST /api/v1/admin/customers/{id}/reinstate` | ✅ clears suspension |

---

### GROUP I — Admin Rides & Flags

| Endpoint | Test cases |
|---|---|
| `GET /api/v1/admin/rides` | ✅ paginated, filters by status |
| `GET /api/v1/admin/rides/{id}` | ✅ full ride detail + events |
| `GET /api/v1/admin/rides/live` | ✅ only active rides returned (not COMPLETED/CANCELLED) |
| `POST /api/v1/admin/rides/live/{id}/intervene` | ✅ valid action → 200 |
| | ❌ completed ride → 409 |
| `GET /api/v1/admin/flags/gps-anomalies` | ✅ returns anomaly list with `computed_speed_kmh` |
| `GET /api/v1/admin/flags/device-collisions` | ✅ returns devices with `user_count > 1` |

---

### GROUP J — Pricing  (`internal/fare/`)

**File:** `internal/fare/handler_test.go`

| Endpoint | Test cases |
|---|---|
| `GET /api/v1/admin/pricing` | ✅ returns all active configs |
| `POST /api/v1/admin/pricing/{vehicle_type_code}` | ✅ valid config → 201 |
| | ❌ `base_fare_rwf <= 0` → 400 VALIDATION |
| | ❌ `tier1_max_km <= base_distance_km` → 400 VALIDATION |
| | ❌ `night_surcharge_pct > 1.0` → 400 VALIDATION |
| | ❌ `min_fare_rwf < base_fare_rwf` → 400 VALIDATION |
| `GET /api/v1/admin/pricing/{vehicle_type_code}/history` | ✅ returns array of past configs |

---

### GROUP K — Admin Analytics, Revenue, Team, Settings

| Module | Test cases |
|---|---|
| `GET /admin/analytics/overview` | ✅ 200 + numeric fields present |
| `GET /admin/revenue/kpis` | ✅ 200 + `total_revenue_rwf` field |
| `GET /admin/dashboard` | ✅ 200 in < 500ms (cached) |
| `GET /admin/team` | ✅ returns team members list |
| `POST /admin/team/invite` | ✅ valid email+role → 201 |
| | ❌ duplicate email → 409 |
| `GET /admin/settings` | ✅ returns all config sections |
| `PUT /admin/settings/commission` | ✅ valid pct → 200 |
| | ❌ pct > 100 → 400 |

---

### GROUP L — WebSocket

**File:** `internal/tracking/hub_test.go`

WebSocket connections require a running server — test via `httptest.NewServer`:

```go
func TestCustomerWS_RequiresRideID(t *testing.T) {
    // Start test server
    srv := httptest.NewServer(router)
    defer srv.Close()

    wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws/customer"

    // Missing ride_id → server closes immediately
    conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token=VALID_TOKEN", nil)
    // Should get 400 Bad Request before upgrade
    assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
```

---

## 6. Running Tests in CI

The pipeline runs this exact command — your tests must pass it:

```bash
go test -race -count=1 ./...
```

**`-race`** — detects data races. Never use shared mutable state in goroutines without a mutex.  
**`-count=1`** — disables test result caching. Tests always re-run.

### Adding a new test file

1. Match the package name: if testing `internal/admin/`, use `package admin_test`
2. Import testify: `"github.com/stretchr/testify/assert"` and `"github.com/stretchr/testify/require"`
3. Filename: `<thing>_test.go` — e.g. `handler_test.go`, `service_test.go`

### Zero tolerance rules

- `t.Fatal` / `require.NoError` for setup failures (test can't continue)
- `assert.Equal` for checks (test continues, collects all failures)
- Never `time.Sleep` in tests — use channels or `testify/mock`'s `Wait`
- Never hardcode UUIDs from a real DB — generate with `uuid.New()`
- Always use `t.Cleanup()` to release resources (miniredis, temp files)

---

## 7. Useful Test Utilities Already Available

| Utility | Location | Purpose |
|---|---|---|
| `makeJWT(t, secret, userID, roleState, tokenType, expiry)` | `test/integration/endpoints_test.go` | Mint a signed JWT for any role |
| `jsonBody(t, v)` | `test/integration/endpoints_test.go` | Marshal struct → `io.Reader` for request body |
| `decodeData(t, rr, &target)` | `test/integration/endpoints_test.go` | Unwrap `{"data": ...}` envelope |
| `newTestRedis(t)` | `test/e2e/flows_test.go` | Start miniredis, auto-cleanup |
| `geo.DistanceKM(a, b)` | `pkg/geo/geo.go` | Haversine distance — use in location tests |
| `location.Geohash6(lat, lng)` | `internal/location/service.go` | Build cache keys in tests |

---

## 8. Kigali Test Coordinates

Always use real Kigali coordinates — San Francisco defaults will fail location radius checks:

```go
var (
    KigaliCBD        = geo.Point{Lat: -1.9441, Lng: 30.0619}
    Kimironko        = geo.Point{Lat: -1.9322, Lng: 30.1044}
    NyabugogoBusPark = geo.Point{Lat: -1.9706, Lng: 30.0586}
    KicukiroCenter   = geo.Point{Lat: -2.0007, Lng: 30.0618}
    Remera           = geo.Point{Lat: -1.9565, Lng: 30.1024}
)
```

---

## 9. Definition of Done

A test suite is complete when:

- [ ] Every endpoint in Section 5 has at least: happy path + missing auth + one validation failure
- [ ] `go test -race -count=1 ./...` passes with **zero failures**
- [ ] No test takes more than **2 seconds** (if it does, you're hitting a real DB — fix it)
- [ ] Coverage for `internal/admin/` ≥ **70%** (`go test -cover ./internal/admin/...`)
- [ ] Coverage for `internal/fare/` ≥ **80%** (pure logic, easy to cover)
- [ ] PR opened against `dev` with title `test: <module> endpoint coverage`

---

## 10. How to Open Your PR

```bash
git checkout dev
git pull origin dev
git checkout -b test/admin-endpoints

# ... write tests ...

git add .
git commit -m "test: admin driver lifecycle endpoint coverage"
git push origin test/admin-endpoints
# Then open PR on GitHub → target: dev
```

The PR will be reviewed by @pac-cee before merge.

---

*Last updated: 2026-05-29 — reflects `dev` branch at commit `b4465bc`*
