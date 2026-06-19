# System Design

## Architecture Style

Rides backend uses a modular monolith architecture.

Why this fits the MVP:

- One deployable service is faster to build and operate.
- Domain modules keep the code organized without microservice overhead.
- Shared transactions and state-machine rules are easier inside one process.
- Future services can be extracted later around stable boundaries: matching, analytics, notification, payments.

## High-Level Runtime

```text
Mobile Apps
  -> HTTP API / WebSocket
  -> Go API container
  -> PostgreSQL/PostGIS, Redis, object storage, external providers
```

## Major Components

| Component | Responsibility |
|---|---|
| `cmd/server` | App entry point, dependency wiring, router setup, migrations, HTTP server lifecycle. |
| `config` | Environment-driven runtime config. |
| `internal/auth` | OTP, JWTs, sessions, device sessions, registration/login. |
| `internal/customer` | Customer profile. |
| `internal/driver` | Driver applications, documents, availability, GPS updates, stats, earnings. |
| `internal/ride` | Ride state machine, lifecycle transitions, cancellation, completion. |
| `internal/matching` | Redis GEO/PostGIS candidate search and sequential offer loop. |
| `internal/negotiation` | Fare offers, manual lock, call initiation, fare confirmation. |
| `internal/location` | Saved locations, landmarks, route cache, fare data aggregation. |
| `internal/tracking` | WebSocket hub for customers and drivers. |
| `internal/admin` | Admin review, suspensions, audits, analytics access. |
| `internal/analytics` | Event publishing/consuming and read models. |
| `internal/middleware` | Authentication, role guards, rate limits, logging. |
| `pkg/*` | Shared utilities: errors, responses, geo, Redis keys, Postgres, logger. |

## Layering Pattern

```text
Handler -> Service -> Repository -> Database/Redis
```

Handlers:

- Decode and validate request bodies.
- Read route params and JWT claims.
- Call service methods.
- Return via `pkg/respond`.

Services:

- Own business logic.
- Enforce state and role-sensitive rules.
- Publish analytics and WebSocket events.
- Coordinate Redis and repository writes.

Repositories:

- Own SQL.
- Convert database rows into domain structs.
- Hide Postgres/PostGIS details from services.

## Request Lifecycle

1. Chi routes request.
2. Middleware attaches request id, logs request, authenticates JWT, and checks roles.
3. Handler validates request.
4. Service applies business rules and state transitions.
5. Repository persists/query data.
6. Redis is updated for hot state when needed.
7. Analytics and WebSocket notifications are emitted.
8. Handler returns JSON envelope or no-content status.

## State Management

| State Type | Storage |
|---|---|
| Durable records | PostgreSQL/PostGIS |
| Driver live availability | Redis |
| Driver GEO index | Redis GEO |
| Active ride pointers | Redis |
| JWT refresh sessions | Redis |
| OTP rate limits | Redis |
| Route cache | PostgreSQL + Redis hot cache |
| Ride events | PostgreSQL |
| Analytics events | Redis stream + PostgreSQL |

## Scalability Design

The API container is mostly stateless. Scaling horizontally is possible if all instances share:

- Same PostgreSQL database.
- Same Redis cluster.
- Same JWT secrets.
- Same external provider credentials.

Important scaling concern:

- WebSocket connections are process-local today. With more than one API instance, either sticky sessions or a shared pub/sub fanout is needed for reliable cross-instance WebSocket delivery.

## Security Boundaries

| Boundary | Protection |
|---|---|
| Public auth | OTP rate limit. |
| Customer routes | JWT + customer/driver pending/active role where allowed. |
| Driver routes | JWT + driver role requirements. |
| Admin routes | JWT + admin role. |
| Sessions | Redis refresh-session validation. |
| Ride data | Repository queries scope by customer or driver where appropriate. |
| Sensitive config | Environment variables, not code. |

## Deployment Units

| Unit | Runtime |
|---|---|
| API | Docker container running Go binary. |
| Database | PostgreSQL with PostGIS. |
| Cache | Redis. |
| Object storage | MinIO locally; cloud object storage/CDN in production. |
| External providers | Africa's Talking, Firebase, future payment provider. |

## Known Design Risks

- WebSocket fanout is not cluster-safe yet.
- Test coverage is still low.
- Some admin workflows are basic and need more audit/detail screens.
- OpenAPI response schemas are not complete.
- Production observability needs metrics, tracing, and dashboards.
