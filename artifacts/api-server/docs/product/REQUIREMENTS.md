# Requirements Matrix

## MVP Acceptance Goal

The MVP is acceptable when a customer and driver can complete the main ride journey:

1. Customer registers and gets a token.
2. Driver registers, applies, accepts policy, is approved by admin, goes online, and sends location.
3. Customer sees nearby vehicle availability and creates a ride.
4. Matching offers the ride to an available driver.
5. Driver accepts and both parties negotiate or manually lock fare.
6. Driver goes en-route, arrives, starts, and completes ride.
7. System records ride events, fare, route cache data, and releases driver.
8. Customer sees ride in history and driver sees payout-based earnings.

## Customer Journey Requirements

| Area | Requirement | Backend Endpoint/Module | Status |
|---|---|---|---|
| Onboarding | Register with name and Rwanda phone. | `POST /api/v1/auth/register` | Ready |
| Onboarding | Verify 6-digit OTP. | `POST /api/v1/auth/verify-otp` | Ready |
| Home | Fetch profile/greeting data. | `GET /api/v1/customer/profile` | Ready |
| Home | Get nearby drivers by vehicle. | `POST /api/v1/customer/location` | Ready |
| Destination | Use saved locations. | `/api/v1/users/me/saved-locations` | Ready |
| Destination | Use landmarks. | `GET /api/v1/locations/landmarks` | Ready |
| Destination | Use recent destinations. | `GET /api/v1/locations/suggestions` | Ready |
| Route | Get/store route distance and duration. | `/api/v1/locations/route` | Ready |
| Booking | Create ride request. | `POST /api/v1/customer/rides` | Ready |
| Booking | Cancel search. | `DELETE /api/v1/customer/rides/{ride_id}` | Ready |
| Negotiation | Propose/accept/decline fare. | `/customer/rides/{ride_id}/negotiation/*` | Ready |
| Active Ride | Reconnect to active ride. | `GET /api/v1/rides/active` | Ready |
| Tracking | Receive ride events. | `WS /ws/customer?ride_id=` | Ready |
| Completion | List ride history. | `GET /api/v1/customer/rides` | Ready |

## Driver Journey Requirements

| Area | Requirement | Backend Endpoint/Module | Status |
|---|---|---|---|
| Apply | Submit personal/vehicle/payment/location fields. | `POST /api/v1/driver/apply` | Ready |
| Documents | Submit document URLs. | `POST /api/v1/driver/documents` | Ready |
| Policy | Accept driver policy as MVP boolean. | `POST /api/v1/driver/policy/accept` | Ready |
| Approval | Admin activates driver. | `POST /api/v1/admin/drivers/{id}/approve` | Ready |
| Availability | Go online/offline. | `POST /api/v1/driver/availability` | Ready |
| Location | Send GPS by HTTP or WS. | `POST /api/v1/driver/location`, `WS /ws/driver` | Ready |
| Request | Receive ride request. | WebSocket hub + FCM optional | Ready |
| Request | Accept or decline. | `/driver/rides/{ride_id}/accept`, `/decline` | Ready |
| Negotiation | Propose/accept/decline fare. | `/driver/rides/{ride_id}/negotiation/*` | Ready |
| Negotiation | Lock verbal fare. | `/driver/rides/{ride_id}/negotiation/lock-fare` | Ready |
| Pickup | Mark en-route. | `/driver/rides/{ride_id}/en-route` | Ready |
| Pickup | Mark arrived and start wait timer. | `/driver/rides/{ride_id}/arrive` | Ready |
| Pickup | Cancel after pickup expiry without penalty. | `/driver/rides/{ride_id}/cancel` | Ready |
| Journey | Start ride. | `/driver/rides/{ride_id}/start` | Ready |
| Journey | Complete ride and optionally submit final generic destination. | `/driver/rides/{ride_id}/complete` | Ready |
| Earnings | Show payout after platform fee. | `/driver/earnings/daily`, `/weekly` | Ready |

## Admin Requirements

| Area | Requirement | Endpoint | Status |
|---|---|---|---|
| Drivers | List applications. | `GET /api/v1/admin/drivers` | Ready |
| Drivers | Approve/reject/suspend driver. | `/admin/drivers/{id}/*` | Ready |
| Users | List/suspend users. | `/admin/users` | Ready |
| Audits | Device collision and GPS anomaly checks. | `/admin/flags/*` | Ready |
| Rides | List rides. | `GET /api/v1/admin/rides` | Ready |
| Analytics | Overview, ride counts, revenue, heatmap, cancellations. | `/admin/analytics/*` | Ready |
| Documents | Admin review of uploaded driver documents. | TBD | Planned |

## Non-Functional Requirements

| ID | Requirement | Current Support |
|---|---|---|
| NFR-001 | API response consistency. | `pkg/respond` envelope. |
| NFR-002 | Auth and role protection. | JWT middleware + `RequireRole`. |
| NFR-003 | Geospatial matching performance. | Redis GEO hot path + PostGIS fallback. |
| NFR-004 | Graceful local development. | Docker Compose + Makefile + `.env.example`. |
| NFR-005 | Auditable ride lifecycle. | `ride_events` append log. |
| NFR-006 | Analytics collection. | Redis stream + `analytics_events`. |
| NFR-007 | Deployment portability. | Dockerfile and env-driven config. |
| NFR-008 | Production observability. | Structured logs exist; metrics/tracing still needed. |

## Deferred Items

- Customer policy acceptance.
- Admin document review workflow.
- Rich OpenAPI schemas for every response body.
- Full integration test harness with real Postgres/Redis containers.
- Production object storage wiring and signed upload URLs.
- Metrics, tracing, alerting dashboards.
