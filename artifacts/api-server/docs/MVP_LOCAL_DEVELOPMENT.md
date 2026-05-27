# MVP Local Development Runbook

This backend is being built as a fast, iterative MVP. The goal for local development is simple: any teammate should be able to start the API, run tests, inspect Swagger, and verify the customer and driver journeys without guessing the project conventions.

## Quick Start

From `artifacts/api-server`:

```bash
make env
make db-up
make dev
```

Then in another terminal:

```bash
make smoke
make test
make coverage
```

Local URLs:

| Purpose | URL |
|---|---|
| API base | `http://localhost:8080/api/v1` |
| Health | `http://localhost:8080/health` |
| Swagger UI | `http://localhost:8080/swagger` |
| Swagger JSON | `http://localhost:8080/swagger/openapi.json` |
| Driver WS | `ws://localhost:8080/ws/driver` |
| Customer WS | `ws://localhost:8080/ws/customer?ride_id={ride_id}` |

## Local Config

Use `.env.example` as the local default. Optional providers are intentionally blank:

| Provider | Local behavior |
|---|---|
| Africa's Talking | OTP is created and logged in dev; SMS send is skipped when credentials are blank. |
| Firebase | Push is disabled when `FIREBASE_SERVICE_ACCOUNT_PATH` is blank. WebSocket events still work. |
| Google/Mapbox | Route search, geocoding, and autocomplete are mobile/client responsibilities for MVP. |
| MoMo/Airtel | Payment setup fields are collected; real payout integration can be iterated later. |
| Documents | API stores document URLs. Local MinIO runs on `http://localhost:9001` for dev, then production can swap to a real CDN. |

For local Docker, `docker-compose.yml` overrides Postgres and Redis URLs to container hostnames. For `go run`, `.env.example` points to localhost.

## Useful Commands

| Command | Use |
|---|---|
| `make env` | Create `.env` if it does not exist. |
| `make db-up` | Start Postgres/PostGIS and Redis for local `go run`. |
| `make dev` | Run the API locally. |
| `make docker-up` | Run Postgres, Redis, and API in Docker. |
| `make test` | Run the full Go test suite. |
| `make coverage` | Produce `coverage.out` and total coverage. |
| `make swagger-check` | Validate Swagger JSON with `jq`. |
| `make smoke` | Verify `/health` and Swagger JSON against a running server. |

## API Patterns

Use the existing module pattern:

```text
handler -> service -> repository
```

Handlers:

- Decode JSON, validate request shape, read route params and JWT claims.
- Return responses through `pkg/respond`.
- Do not place business rules here.

Services:

- Own business decisions, state transitions, Redis updates, analytics events, WebSocket notifications.
- Return typed errors from `pkg/errors` where possible.
- Keep driver/customer/admin rules explicit.

Repositories:

- Own SQL and Postgres/PostGIS access.
- Keep DB writes atomic where state can race.
- Return domain records or typed errors.

Responses:

```json
{
  "data": {}
}
```

Errors:

```json
{
  "error": {
    "code": "BAD_REQUEST",
    "message": "invalid request"
  }
}
```

Protection:

- Public endpoints: auth register, verify OTP, refresh, public landmarks, health, Swagger.
- Protected endpoints: wrap route groups with `mw.Authenticate(cfg, rdb)`.
- Role-specific endpoints: add `mw.RequireRole(...)` after authentication.
- Admin endpoints: always require `mw.RequireRole(mw.RoleAdmin)`.

## MVP Journey Readiness

Legend:

| Status | Meaning |
|---|---|
| Ready | Backend endpoint/service exists for MVP use. |
| Client | Mobile app owns this behavior. |
| Partial | Usable, but needs follow-up hardening. |
| Gap | Needed before the listed journey is truly complete. |

### Customer Flow

| Step | Status | Backend support |
|---|---|---|
| Register with Rwanda phone and OTP | Ready | `POST /auth/register`, `POST /auth/verify-otp`; OTP logs in dev. |
| Customer policy acceptance | Gap | No customer policy acceptance endpoint yet. |
| GPS permission, map, reverse geocode, notification permission | Client | Mobile app + Mapbox/OS permissions. |
| Nearby drivers on map | Ready | `POST /customer/location`; driver locations come from Redis GEO. |
| Vehicle chips | Client | Backend supports `MOTO_BIKE`, `CAB_TAXI`, `LIGHT_HILUX`, `HEAVY_FUSO`. |
| Destination search, landmark, saved, recent | Partial | Saved/landmarks/recent supported; Mapbox autocomplete is client-side. |
| Route preview | Ready | `GET/POST /locations/route`; cache-first data, no fare suggestion returned. |
| Generic destination | Ready | Client can submit final dropoff coordinates on complete so the backend stores the real endpoint. |
| Find driver | Ready | `POST /customer/rides`; matching engine starts. |
| Searching and cancel search | Ready | Ride state `SEARCHING`; `DELETE /customer/rides/{ride_id}`. |
| Fare negotiation | Ready | Customer propose/accept/decline endpoints; max 3 offers per side. |
| Confirmation modal | Client | Backend confirmation happens when an offer/manual fare is accepted. |
| Active ride reconnect | Ready | `GET /rides/active`. |
| Driver live location | Partial | WebSocket hub exists; mobile must keep driver WS connected and send updates. |
| Driver arrived banner | Ready | Driver can call `POST /driver/rides/{ride_id}/arrive`; customer receives WS event. |
| Pickup expiry | Ready | Server timer sets `pickup_expired`; driver can cancel after expiry without decline penalty. |
| Journey in progress | Ready | `POST /driver/rides/{ride_id}/start`. |
| Ride completed | Ready | `POST /driver/rides/{ride_id}/complete`; agreed fare is recorded into route cache when available. |
| Ride history | Ready | `GET /customer/rides`. |

### Driver Flow

| Step | Status | Backend support |
|---|---|---|
| Join as driver | Ready | `POST /driver/apply`. |
| Rwanda admin cascade | Client | Local mobile data, no API required. |
| Vehicle info | Ready | Driver application captures transport and vehicle fields. |
| Document uploads | Partial | API stores document URLs; local MinIO is available for development uploads. |
| Payment setup | Partial | Payment fields are collected; real payout integration later. |
| Policy acceptance | Ready | `POST /driver/policy/accept`. |
| Admin approval | Ready | `POST /admin/drivers/{id}/approve`. |
| Driver dashboard stats | Ready | `/driver/stats`, `/driver/earnings/daily`, `/driver/earnings/weekly`; earnings return driver payout after 15% platform commission. |
| Go online | Ready | `POST /driver/availability`, `POST /driver/location`; Redis GEO is updated. |
| Ride request popup | Ready | Matching sends driver WebSocket + optional FCM. |
| Accept/decline request | Ready | `/driver/rides/{ride_id}/accept`, `/driver/rides/{ride_id}/decline`. |
| Fare negotiation | Ready | Driver propose/accept/decline endpoints. |
| Manual fare lock | Ready | `POST /driver/rides/{ride_id}/negotiation/lock-fare`. |
| Navigate to pickup | Ready | `POST /driver/rides/{ride_id}/en-route`. |
| I have arrived | Ready | `POST /driver/rides/{ride_id}/arrive`. |
| Start journey | Ready | `POST /driver/rides/{ride_id}/start`. |
| Complete ride | Ready | `POST /driver/rides/{ride_id}/complete`; optional final destination coordinates supported for generic/TBD rides. |
| Cancel after pickup expiry | Ready | `POST /driver/rides/{ride_id}/cancel` checks `pickup_expired=true` and avoids decline penalty. |

## Before Each MVP Deploy

Run:

```bash
make swagger-check
make test
make coverage
make docker-up
make smoke
```

Then manually verify:

- Swagger opens and shows new endpoints.
- Register/OTP works in dev logs.
- A driver can apply, accept policies, be approved, go online, and update location.
- A customer can create a ride and reach negotiation.
- Driver can accept, negotiate or manually lock fare, go en-route, arrive, start, and complete.
- Driver can cancel after pickup expiry for customer no-show.
- Customer can reconnect using `/rides/active` and later list ride history.

## Known MVP Follow-Ups

- Add customer policy acceptance persistence.
- Add richer OpenAPI response schemas, not only descriptions.
- Add integration tests for the full customer/driver happy path against Postgres and Redis.
- Raise total coverage beyond smoke/helper tests before a serious production rollout.
