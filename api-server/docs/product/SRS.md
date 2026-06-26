# Software Requirements Specification

## 1. Purpose

Rides is a ride-hailing and light logistics backend for Rwanda and East Africa. It supports customers booking transport, drivers accepting and completing rides, negotiation-based fare agreement, admin oversight, and operational analytics.

This document defines the MVP backend requirements and system expectations.

## 2. Product Scope

The MVP backend must support:

- Customer registration and login using phone number OTP.
- Customer profile and saved locations.
- Nearby driver discovery by vehicle type.
- Ride creation, cancellation, matching, negotiation, confirmation, journey progress, completion, and history.
- Driver onboarding, document URL capture, policy acceptance, availability, GPS updates, ride handling, stats, and earnings.
- Admin approval/suspension/audit/analytics endpoints.
- Real-time ride events through WebSockets.
- Route cache collection and agreed-fare aggregation for later pricing intelligence.
- Local development environment with Postgres/PostGIS, Redis, and MinIO.

## 3. Actors

| Actor | Description |
|---|---|
| Customer | Registers, books rides, negotiates fare, tracks driver, views ride history. |
| Driver/Rider | Applies, uploads document URLs, accepts policy, goes online, accepts rides, negotiates fare, completes rides. |
| Admin | Reviews drivers, suspends users/drivers, monitors GPS/device flags, views analytics. |
| System | Runs matching, state transitions, Redis cleanup, pickup expiry, analytics events, notifications. |
| DevOps | Deploys, monitors, configures secrets, manages database migrations, CI/CD, rollback. |

## 4. Functional Requirements

### Authentication

| ID | Requirement | Status |
|---|---|---|
| AUTH-001 | User can register/login with E.164 phone number and OTP. | Implemented |
| AUTH-002 | OTP must be 6 digits and expire. | Implemented |
| AUTH-003 | Access/refresh JWTs are issued after OTP verification. | Implemented |
| AUTH-004 | Refresh sessions are stored in Redis and can be revoked. | Implemented |
| AUTH-005 | Device sessions are logged for collision detection. | Implemented |

### Customer

| ID | Requirement | Status |
|---|---|---|
| CUST-001 | Customer can read and update profile. | Implemented |
| CUST-002 | Customer can request nearby anonymized drivers by vehicle type. | Implemented |
| CUST-003 | Customer can create a ride request. | Implemented |
| CUST-004 | Customer can cancel cancellable rides. | Implemented |
| CUST-005 | Customer can list/get own rides. | Implemented |
| CUST-006 | Customer can manage saved locations. | Implemented |
| CUST-007 | Customer can fetch active ride after reconnect. | Implemented |
| CUST-008 | Customer app can use landmarks/recent/saved suggestions. | Implemented |
| CUST-009 | Customer policy acceptance. | Deferred |

### Driver/Rider

| ID | Requirement | Status |
|---|---|---|
| DRV-001 | User can apply as driver with personal, vehicle, location, and payment setup fields. | Implemented |
| DRV-002 | Driver can upload document URLs. | Implemented |
| DRV-003 | Driver can accept platform policies as one MVP boolean. | Implemented |
| DRV-004 | Driver can go online/offline. | Implemented |
| DRV-005 | Driver location updates are stored in Redis/Postgres. | Implemented |
| DRV-006 | Driver GPS plausibility is checked. | Implemented |
| DRV-007 | Driver can accept/decline ride requests. | Implemented |
| DRV-008 | Driver can negotiate fare and manually lock fare. | Implemented |
| DRV-009 | Driver can mark en-route, arrived, started, completed. | Implemented |
| DRV-010 | Driver can cancel after pickup expiry without decline penalty. | Implemented |
| DRV-011 | Driver earnings show payout after 15% platform fee. | Implemented |

### Matching

| ID | Requirement | Status |
|---|---|---|
| MATCH-001 | Matching searches Redis GEO by vehicle type. | Implemented |
| MATCH-002 | Matching falls back to PostGIS when Redis is cold. | Implemented |
| MATCH-003 | Candidates are scored by distance, declines, and acceptance rate. | Implemented |
| MATCH-004 | Driver receives one offer at a time with timeout. | Implemented |
| MATCH-005 | Accepted driver is removed from GEO pool and ride moves to negotiation. | Implemented |

### Negotiation

| ID | Requirement | Status |
|---|---|---|
| NEG-001 | Each side can make up to 3 offers. | Implemented |
| NEG-002 | Latest counterparty offer can be accepted. | Implemented |
| NEG-003 | Accepted fare is immutable after lock. | Implemented |
| NEG-004 | Manual fare lock bypasses offer count. | Implemented |
| NEG-005 | Masked call can be initiated. | Implemented |

### Admin

| ID | Requirement | Status |
|---|---|---|
| ADM-001 | Admin can list/approve/reject/suspend drivers. | Implemented |
| ADM-002 | Admin can list/suspend users. | Implemented |
| ADM-003 | Admin can inspect GPS anomalies and device collisions. | Implemented |
| ADM-004 | Admin can view rides and analytics summaries. | Implemented |
| ADM-005 | Admin document review workflow. | Planned |

## 5. Non-Functional Requirements

| Category | Requirement |
|---|---|
| Availability | API should be deployable as stateless containers behind a load balancer. |
| Performance | Hot matching state should use Redis; PostGIS fallback must remain indexed. |
| Reliability | Ride state transitions must be guarded and invalid transitions rejected. |
| Security | JWT-authenticated endpoints must validate Redis session state and role authorization. |
| Privacy | Customers must not see driver identity until ride is confirmed/allowed by business logic. |
| Auditability | Ride events and analytics events should be append-only where possible. |
| Observability | Logs should be structured; future metrics/tracing should be added before production scale. |
| Maintainability | Code should follow `handler -> service -> repository` module boundaries. |
| Portability | Local dev uses Docker Compose; production should use managed Postgres/Redis/object storage. |
| Testability | Critical business rules require unit tests and integration tests. |

## 6. External Dependencies

| Dependency | Use |
|---|---|
| PostgreSQL/PostGIS | Persistent data, geospatial checks, ride history, audits. |
| Redis | Sessions, rate limits, matching state, driver GEO index, hot ride state. |
| Africa's Talking | OTP SMS and masked calling. |
| Firebase Cloud Messaging | Mobile push notifications. |
| MinIO/local CDN | Local development object storage for document URLs. |
| Mapbox/mobile maps | Client-side autocomplete, directions, map rendering, reverse geocoding. |

## 7. Assumptions

- Mobile apps own map rendering, autocomplete, permissions, and visual journey screens.
- Admin/business-owner workflows will evolve separately.
- Payments/payouts are collected as metadata for MVP; real payment execution is later.
- Driver documents are stored externally and the API stores only URLs.

## 8. Constraints

- All v1 API routes must remain under `/api/v1`.
- Fare suggestions must not be exposed publicly yet.
- Ride state transitions must use the central state machine.
- Production secrets must never be committed.
