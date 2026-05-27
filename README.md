# Taravelis — Ride-Hailing Backend API

Go backend for the Taravelis ride-hailing platform (Kigali, Rwanda).
Supports motos, cabs, hilux pickups, and fuso trucks.

---

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.22 |
| HTTP Router | go-chi/chi v5 |
| Database | PostgreSQL 16 + PostGIS |
| Cache / real-time state | Redis 7 |
| WebSockets | Gorilla WebSocket |
| Auth | JWT (access + refresh) + OTP via Africa's Talking |
| Push notifications | Firebase Cloud Messaging |
| Payments | MTN MoMo + Airtel Money |
| Migrations | golang-migrate |

---

## Project Structure

```
artifacts/api-server/
├── cmd/server/main.go        # Entry point, dependency wiring, inline handlers
├── config/config.go          # Env-based config
├── internal/
│   ├── auth/                 # OTP, JWT, sessions
│   ├── customer/             # Customer profile
│   ├── driver/               # Driver profile, location, documents
│   ├── ride/                 # Ride lifecycle, state machine, geo-gates
│   ├── matching/             # 15s goroutine matching engine
│   ├── negotiation/          # Fare negotiation rounds
│   ├── tracking/             # WebSocket hub (driver + customer)
│   ├── notification/         # FCM push
│   ├── analytics/            # Event stream, admin analytics
│   ├── admin/                # Admin management
│   ├── telephony/            # Africa's Talking OTP + masked calls
│   ├── payment/              # MTN MoMo + Airtel Money
│   └── middleware/           # JWT auth, role enforcement, rate limiting
├── migrations/               # 001–011 up/down SQL pairs
├── pkg/
│   ├── geo/                  # PostGIS helpers, distance utils
│   ├── redis/                # Redis client + all key patterns
│   ├── postgres/             # Connection pool
│   ├── logger/               # zerolog structured logging
│   ├── errors/               # Typed AppError sentinels
│   └── respond/              # JSON response helpers
├── Dockerfile
├── docker-compose.yml
├── .env.example
└── go.mod
```

---

## Running with Docker (recommended)

For the current MVP workflow, see the local runbook:
[`artifacts/api-server/docs/MVP_LOCAL_DEVELOPMENT.md`](artifacts/api-server/docs/MVP_LOCAL_DEVELOPMENT.md)

### 1. Copy and configure env

```bash
cd artifacts/api-server
cp .env.example .env
```

Edit `.env` and fill in at minimum:
```env
JWT_ACCESS_SECRET=<generate with: openssl rand -hex 64>
JWT_REFRESH_SECRET=<generate with: openssl rand -hex 64>
```

Everything else can stay as-is for local dev. AT and Firebase credentials are optional — the server degrades gracefully without them (OTP prints to logs in dev mode).

### 2. Start everything

```bash
cd artifacts/api-server
docker compose up --build
```

This starts:
- `ride_postgres` — PostgreSQL 16 + PostGIS on port 5432
- `ride_redis` — Redis 7 on port 6379
- `ride_api` — Go API on port 8080

Migrations run automatically on startup.

### 3. Verify

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### Stop

```bash
docker compose down
```

To also wipe the database volumes:
```bash
docker compose down -v
```

---

## Running Locally (without Docker)

Requires: Go 1.22+, PostgreSQL 16 with PostGIS, Redis 7

### 1. Start Postgres and Redis

```bash
# Postgres with PostGIS must be running and have the extension:
psql -U postgres -c "CREATE DATABASE rideplatform;"
psql -U postgres -d rideplatform -c "CREATE EXTENSION IF NOT EXISTS postgis;"

# Redis
redis-server
```

### 2. Configure env

```bash
cd artifacts/api-server
cp .env.example .env
# Edit DATABASE_URL, REDIS_URL, JWT secrets
```

### 3. Run

```bash
cd artifacts/api-server
go run ./cmd/server
```

---

## API Overview

Base URL: `http://localhost:8080/api/v1`

Swagger UI: `http://localhost:8080/swagger`

### Auth (public)

| Method | Path | Description |
|---|---|---|
| POST | `/auth/register` | Send OTP — body: `phone_number`, `full_name`, `device_id`, `platform` |
| POST | `/auth/verify-otp` | Verify OTP → returns `access_token`, `refresh_token`, `role_state` |
| POST | `/auth/refresh` | Refresh access token |
| POST | `/auth/logout` | Revoke session |

### Customer

| Method | Path | Description |
|---|---|---|
| GET | `/customer/profile` | Get profile |
| PUT | `/customer/profile` | Update `full_name`, `email`, `fcm_token` |
| POST | `/customer/location` | Get nearby drivers (anonymised) |
| POST | `/customer/rides` | Book a ride → triggers matching engine |
| GET | `/customer/rides` | Ride history |
| GET | `/customer/rides/:id` | Get ride (driver hidden until CONFIRMED) |
| DELETE | `/customer/rides/:id` | Cancel ride |
| POST | `/customer/rides/:id/negotiation/propose` | Propose fare |
| POST | `/customer/rides/:id/negotiation/accept` | Accept fare |
| POST | `/customer/rides/:id/negotiation/decline` | Decline fare |

### Driver

| Method | Path | Description |
|---|---|---|
| POST | `/driver/apply` | Submit driver application |
| GET | `/driver/profile` | Get driver profile |
| PUT | `/driver/profile` | Update city, momo_pay_code, momo_provider, fcm_token |
| POST | `/driver/policy/accept` | Accept all 5 platform policies |
| POST | `/driver/documents` | Upload document — body: `document_type`, `file_url` |
| GET | `/driver/documents` | List uploaded documents |
| POST | `/driver/availability` | Toggle online/offline |
| POST | `/driver/location` | Update GPS location |
| POST | `/driver/rides/:id/accept` | Accept ride request (15s TTL enforced) |
| POST | `/driver/rides/:id/decline` | Decline ride request |
| POST | `/driver/rides/:id/en-route` | Start navigation → CONFIRMED → DRIVER_EN_ROUTE |
| POST | `/driver/rides/:id/start` | Start ride → geo-gate ≤150m from pickup |
| POST | `/driver/rides/:id/complete` | Complete ride → geo-gate ≤200m from destination |
| POST | `/driver/rides/:id/negotiation/propose` | Propose fare |
| POST | `/driver/rides/:id/negotiation/accept` | Accept fare |
| POST | `/driver/rides/:id/negotiation/decline` | Decline fare |
| POST | `/driver/rides/:id/negotiation/initiate-call` | Get masked phone number for call |
| GET | `/driver/earnings/daily` | Today's earnings |
| GET | `/driver/earnings/weekly` | Last 7 days earnings |
| GET | `/driver/stats` | total_rides, acceptance_rate, completion_rate, priority_tier |

### Admin

| Method | Path | Description |
|---|---|---|
| GET | `/admin/drivers` | List drivers (filter by `?status=`) |
| POST | `/admin/drivers/:id/approve` | Approve driver |
| POST | `/admin/drivers/:id/reject` | Reject driver |
| POST | `/admin/drivers/:id/suspend` | Suspend driver |
| GET | `/admin/users` | List users |
| POST | `/admin/users/:id/suspend` | Suspend user |
| GET | `/admin/flags/gps-anomalies` | GPS anomaly log |
| GET | `/admin/flags/device-collisions` | Multi-account device flags |
| GET | `/admin/rides` | All rides |
| GET | `/admin/analytics/overview` | Platform overview |
| GET | `/admin/analytics/rides/daily` | Daily ride counts |
| GET | `/admin/analytics/rides/weekly` | Weekly ride counts |
| GET | `/admin/analytics/revenue/breakdown` | Revenue by vehicle type |
| GET | `/admin/analytics/drivers/performance` | Driver performance table |
| GET | `/admin/analytics/negotiation/stats` | Negotiation round stats |
| GET | `/admin/analytics/heatmap` | Pickup demand heatmap |
| GET | `/admin/analytics/cancellations` | Cancellation stats |

### WebSocket

| Path | Role | Description |
|---|---|---|
| `WS /ws/driver` | DRIVER_ACTIVE | Send location updates, receive ride requests |
| `WS /ws/customer?ride_id=` | CUSTOMER | Receive driver location, arrival, completion events |

---

## Document Types

For `POST /driver/documents`:

| `document_type` | Description |
|---|---|
| `LICENCE_FRONT` | Driver's licence front face |
| `VEHICLE_INSURANCE` | Vehicle insurance document |
| `VEHICLE_AUTHORIZATION` | Vehicle authorization / inspection certificate |

---

## Ride State Machine

```
SEARCHING → MATCHED → NEGOTIATING → CONFIRMED → DRIVER_EN_ROUTE
         → DRIVER_ARRIVED → IN_PROGRESS → COMPLETED
         → CANCELLED (from most states)
```

---

## Environment Variables

See `.env.example` for the full list with descriptions.
