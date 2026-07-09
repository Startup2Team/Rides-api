# API Reference

Base path: `/api/v1`

All request/response bodies are JSON. All protected endpoints require a `Authorization: Bearer <access_token>` header.

---

## Authentication

### Role States

| Value | Description |
|---|---|
| `CUSTOMER_ONLY` | Regular customer — no driver profile |
| `DRIVER_PENDING` | Applied as driver, awaiting admin approval |
| `DRIVER_ACTIVE` | Approved driver |
| `DRIVER_SUSPENDED` | Suspended driver |
| `ADMIN` | Platform administrator |

### Role Guards on Routes

| Route Group | Required Role |
|---|---|
| `/customer/*` | `CUSTOMER_ONLY`, `DRIVER_PENDING`, or `DRIVER_ACTIVE` |
| `/driver/apply` | Any authenticated user |
| `/driver/profile`, `/driver/documents` | `DRIVER_PENDING` or `DRIVER_ACTIVE` |
| `/driver/availability`, `/driver/location`, ride actions, earnings | `DRIVER_ACTIVE` only |
| `/admin/*` | `ADMIN` |

---

## Public Endpoints (no auth)

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Health check → `{"status":"ok"}` |
| `GET` | `/swagger` | Swagger UI (HTML) |
| `GET` | `/swagger/openapi.json` | OpenAPI spec |
| `GET` | `/api/v1/pricing` | Public list of active pricing configs per vehicle type |
| `GET` | `/api/v1/locations/landmarks` | Kigali landmarks |
| `POST` | `/api/v1/admin/auth/login` | Admin login (returns JWT or requires 2FA) |
| `POST` | `/api/v1/admin/auth/2fa/verify` | Verify TOTP code → issues admin JWT |
| `POST` | `/api/v1/admin/auth/2fa/backup` | Verify backup code → issues admin JWT |

---

## Auth (`/api/v1/auth`)

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/auth/register` | None | Send OTP to phone. Rate-limited: 5/hour/phone. Dev mode echoes OTP in response. |
| `POST` | `/auth/verify-otp` | None | Verify OTP → creates/updates user → returns `{access_token, refresh_token, user}` |
| `POST` | `/auth/refresh` | None | Exchange refresh token → new access token |
| `POST` | `/auth/logout` | JWT | Revoke refresh session in Redis |

**Register request:**
```json
{ "phone": "+250788123456", "role": "CUSTOMER_ONLY" }
```

**Verify-OTP request:**
```json
{ "phone": "+250788123456", "code": "123456", "device_id": "uuid", "fcm_token": "..." }
```

**Refresh request:**
```json
{ "refresh_token": "..." }
```

---

## Customer (`/api/v1/customer`)

All require JWT. Roles: `CUSTOMER_ONLY`, `DRIVER_PENDING`, `DRIVER_ACTIVE`.

### Profile

| Method | Path | Description |
|---|---|---|
| `GET` | `/customer/profile` | Get own profile (name, phone, email, fcm_token, role_state) |
| `PUT` | `/customer/profile` | Update profile |

### Phone Number Change (OTP-verified)

| Method | Path | Description |
|---|---|---|
| `POST` | `/customer/phone/change/request` | Send an OTP to a new number. Body: `{new_phone}` (E.164). Rejects the current number (`SAME_PHONE`) or one already in use (`409 PHONE_TAKEN`). Rate-limited 5 / 10 min per user. |
| `POST` | `/customer/phone/change/verify` | Verify the OTP and swap the number. Body: `{new_phone, otp}`. On success `users.phone_number` is updated; existing JWTs stay valid. `409 PHONE_TAKEN` on a racing claim, `OTP_*` on a bad/expired code. |

### Loyalty / Gamification

| Method | Path | Description |
|---|---|---|
| `GET` | `/customer/level` | Customer loyalty tier from lifetime **completed** rides. Returns `{level, level_index, completed_rides, total_spend, current_threshold, next_level, next_threshold, rides_to_next_level, progress_to_next (0–1), perks[]}`. Tiers: `BRONZE` (0), `SILVER` (10), `GOLD` (50), `PREMIUM` (150). |

### Nearby Drivers

| Method | Path | Description |
|---|---|---|
| `POST` | `/customer/location` | Get nearby available drivers for a vehicle type. Body: `{lat, lng, transport_type}`. Returns anonymized list with ±165m coordinate jitter. |

### Fare Estimate

| Method | Path | Description |
|---|---|---|
| `GET` | `/customer/fare-estimate` | Estimate fare. Query params: `pickup_lat`, `pickup_lng`, `dest_lat`, `dest_lng`, `vehicle_type`. Returns full `Breakdown` with base/distance/night/waiting. |

### Rides

| Method | Path | Description |
|---|---|---|
| `POST` | `/customer/rides` | Create ride (starts matching) |
| `GET` | `/customer/rides` | List own rides (paginated) |
| `GET` | `/customer/rides/{ride_id}` | Get ride by ID |
| `DELETE` | `/customer/rides/{ride_id}` | Cancel ride (allowed in SEARCHING/MATCHED/NEGOTIATING) |

**Create ride request:**
```json
{
  "transport_type": "MOTO",
  "pickup_lat": -1.9441,
  "pickup_lng": 30.0619,
  "pickup_address": "KG 11 Ave, Kigali",
  "dest_lat": -1.9500,
  "dest_lng": 30.0700,
  "dest_address": "Kimironko Market",
  "customer_initial_fare": 1000
}
```

### Negotiation (Customer Side)

| Method | Path | Description |
|---|---|---|
| `POST` | `/customer/rides/{ride_id}/negotiation/propose` | Submit fare offer. Body: `{amount: 1500}`. Max 3 per side. |
| `POST` | `/customer/rides/{ride_id}/negotiation/accept` | Accept driver's latest proposal. Locks fare → CONFIRMED. |
| `POST` | `/customer/rides/{ride_id}/negotiation/decline` | Decline driver's proposal. |

---

## Driver (`/api/v1/driver`)

### Registration (any authenticated user)

| Method | Path | Description |
|---|---|---|
| `POST` | `/driver/apply` | Submit driver application. Creates `driver_profiles` row with `PENDING_REVIEW`. |

**Apply request:**
```json
{
  "transport_type": "MOTO",
  "vehicle_plate": "RAB123A",
  "license_number": "DL-2024-001",
  "date_of_birth": "1990-01-15",
  "province": "Kigali City",
  "district": "Gasabo",
  "sector": "Remera",
  "cell": "Rukiri I",
  "village": "Inzira",
  "momo_pay_code": "078XXXXXXX",
  "momo_provider": "MTN"
}
```

### Profile & Documents (DRIVER_PENDING or DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `GET` | `/driver/profile` | Get own driver profile |
| `PUT` | `/driver/profile` | Update profile fields |
| `POST` | `/driver/policy/accept` | Accept platform policy |
| `POST` | `/driver/documents` | Upload document. Body: `{document_type, file_url}`. Types: `LICENCE_FRONT`, `VEHICLE_INSURANCE`, `VEHICLE_AUTHORIZATION`. |
| `GET` | `/driver/documents` | List uploaded documents |

### Vehicles & Session (DRIVER_PENDING or DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `GET` | `/driver/session` | One-call bootstrap: driver profile, active vehicle, `vehicle_count`, `has_active_ride`, and `document_alerts` (license/insurance/authorization expiring within 30 days or expired). |
| `GET` | `/driver/vehicles` | List own vehicles (active first). Lazily backfills a vehicle row from the profile for drivers who applied before multi-vehicle existed. |
| `POST` | `/driver/vehicles` | Register another vehicle. Body: `{vehicle_type_code, plate_number, make?, model?, year?, color?, passenger_seats?, load_capacity_kg?}`. First vehicle auto-activates. `409 DUPLICATE_PLATE`. |
| `PATCH` | `/driver/vehicles/{id}` | Update own vehicle (partial). |
| `DELETE` | `/driver/vehicles/{id}` | Remove a vehicle. `409 LAST_VEHICLE` — can't delete the only one; deleting the active one activates the oldest remaining. |
| `POST` | `/driver/vehicles/{id}/activate` | **Switch active vehicle.** Syncs `driver_profiles.transport_type/plate/seats/load` transactionally (matching follows). `403 DRIVER_NOT_APPROVED` unless approved; `409 VEHICLE_SWITCH_ON_RIDE` while a ride is in progress. |

Drivers also get a daily document-expiry notification (push + in-app) at the 30/14/7/3/1/0 day marks and once after expiry.

### Availability & Location (DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `POST` | `/driver/availability` | Toggle online/offline. Body: `{online: true}`. Cooldown: 10 min between toggles. Going offline clears GPS history. |
| `POST` | `/driver/location` | Update GPS position. Body: `{lat, lng}`. Runs plausibility check (>200 km/h → anomaly). Updates Redis GEO index. |
| `GET` | `/driver/demand-heatmap` | Bucketed recent ride-pickup demand (~110 m grid) so a driver can reposition. Query: `lat`,`lng` (optional pair → scope to `radius_km` via PostGIS `ST_DWithin`; omit for busiest cells platform-wide), `window_min` (default 120, clamped 15–1440), `radius_km` (default 5, clamped 0.5–50). Returns `{window_minutes, radius_meters, scoped, points:[{lat,lng,count}]}`. Rate-limited 30/min. |

### Packages & Credits (DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `GET` | `/driver/packages` | List available credit packages |
| `POST` | `/driver/packages/purchase` | Purchase a package. Body: `{package_id}`. |
| `GET` | `/driver/credits` | Get remaining ride credits |

### Ride Actions (DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `GET` | `/driver/rides/active` | Get current active ride |
| `GET` | `/driver/rides/{ride_id}` | Get specific ride (scoped to driver) |
| `POST` | `/driver/rides/{ride_id}/accept` | Accept matching offer. Validates TTL. Checks credits. |
| `POST` | `/driver/rides/{ride_id}/decline` | Decline matching offer. Increments daily decline counter. |
| `POST` | `/driver/rides/{ride_id}/en-route` | Mark as en route → `DRIVER_EN_ROUTE` |
| `POST` | `/driver/rides/{ride_id}/arrive` | Mark arrived at pickup → `DRIVER_ARRIVED`. Starts 5-min expiry timer. |
| `POST` | `/driver/rides/{ride_id}/start` | Start ride → `IN_PROGRESS`. Validates within 150 m of pickup. |
| `POST` | `/driver/rides/{ride_id}/complete` | Complete ride → `COMPLETED`. Optional final destination in body. |
| `POST` | `/driver/rides/{ride_id}/cancel` | Cancel after pickup expiry. Only valid if `pickup_expired=true`. |

**Complete ride body (optional):**
```json
{
  "dest_lat": -1.9550,
  "dest_lng": 30.0680,
  "dest_address": "Final stop"
}
```

### Negotiation (Driver Side)

| Method | Path | Description |
|---|---|---|
| `POST` | `/driver/rides/{ride_id}/negotiation/propose` | Propose fare. Body: `{amount: 2000}`. |
| `POST` | `/driver/rides/{ride_id}/negotiation/accept` | Accept customer's latest proposal. |
| `POST` | `/driver/rides/{ride_id}/negotiation/decline` | Decline customer's proposal. |
| `POST` | `/driver/rides/{ride_id}/negotiation/lock-fare` | Lock fare manually (verbal agreement). Body: `{amount: 1700}`. |
| `POST` | `/driver/rides/{ride_id}/negotiation/initiate-call` | Get masked AT phone number for voice negotiation. |

### Earnings & Stats (DRIVER_ACTIVE)

| Method | Path | Description |
|---|---|---|
| `GET` | `/driver/earnings/daily` | Today's earnings (agreed_fare × 0.85) |
| `GET` | `/driver/earnings/weekly` | Past 7 days earnings |
| `GET` | `/driver/stats` | Total rides, acceptance rate, completion rate, priority tier |

---

## Users (`/api/v1/users`)

Any authenticated user.

| Method | Path | Description |
|---|---|---|
| `PATCH` | `/users/mode` | Switch between customer and driver mode. Body: `{mode: "driver"}`. |
| `GET` | `/users/me/saved-locations` | List saved locations |
| `POST` | `/users/me/saved-locations` | Save a new location. Body: `{label, address, lat, lng}` |
| `PUT` | `/users/me/saved-locations/{id}` | Update saved location |
| `DELETE` | `/users/me/saved-locations/{id}` | Delete saved location |

---

## Locations (`/api/v1/locations`)

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/locations/landmarks` | None | List Kigali landmarks (20 seeded) |
| `GET` | `/locations/suggestions` | JWT | Personalized location suggestions based on history |
| `GET` | `/locations/route` | JWT | Get cached route. Params: `pickup_lat/lng`, `dest_lat/lng`, `vehicle_type`. |
| `POST` | `/locations/route` | JWT | Cache route from mobile. Body: `{pickup, dest, distance_km, duration_minutes, vehicle_type}` |

---

## Uploads (`/api/v1/uploads`)

Any authenticated user.

| Method | Path | Description |
|---|---|---|
| `POST` | `/uploads/presigned-url` | Get an S3/R2 presigned upload URL. Body: `{content_type, filename}`. |

---

## Active Ride Recovery (`/api/v1/rides`)

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/rides/active` | JWT | Reconnect recovery — returns current active ride for either driver or customer |

---

## WebSocket (`/api/v1/ws`)

Both endpoints require JWT passed as `?token=<access_token>`.

| Path | Actor | Direction | Description |
|---|---|---|---|
| `/ws/driver` | Driver | Bidirectional | Receives: `ride_request`, `negotiation_offer`, `ride_confirmed`, `pickup_expired`. Sends: location updates. |
| `/ws/customer` | Customer | Receive-only (from server) | Receives: `driver_matched`, `driver_en_route`, `driver_arrived`, `pickup_expired`, `ride_started`, `ride_completed`, `ride_cancelled`, `negotiation_offer`, `ride_confirmed`. |

---

## Admin (all `/api/v1/admin`, ADMIN role)

### Auth

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/admin/auth/login` | None | Login with email+password |
| `POST` | `/admin/auth/2fa/verify` | None | Verify TOTP code |
| `POST` | `/admin/auth/2fa/backup` | None | Verify backup code |
| `POST` | `/admin/auth/logout` | Admin JWT | Logout |
| `POST` | `/admin/auth/totp/reset` | Admin JWT | Reset TOTP (generates new secret) |

### Dashboard

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/dashboard` | Live platform snapshot: active rides, online drivers, open tickets, revenue, pending verifications, open incidents. Cached every 10 s. |

### Account (self)

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/account` | Get own admin profile |
| `PUT` / `PATCH` | `/admin/account` | Update profile |
| `POST` | `/admin/account/password` | Change password |
| `GET` | `/admin/account/sessions` | List active sessions |
| `DELETE` | `/admin/account/sessions/{sessionId}` | Revoke session |
| `GET` | `/admin/account/2fa/setup` | Get TOTP QR / secret |
| `POST` | `/admin/account/2fa/enable` | Enable 2FA with TOTP code |
| `POST` | `/admin/account/2fa/disable` | Disable 2FA |

### Drivers

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/drivers` | List all drivers (filterable) |
| `GET` | `/admin/drivers/overview` | Driver count by status |
| `GET` | `/admin/drivers/{id}` | Get driver details |
| `PATCH` | `/admin/drivers/{id}` | Update driver fields |
| `DELETE` | `/admin/drivers/{id}` | Delete driver |
| `POST` | `/admin/drivers/{id}/approve` | Approve driver → `ACTIVE`. Auto-grants free trial credits. |
| `POST` | `/admin/drivers/{id}/reject` | Reject application |
| `POST` | `/admin/drivers/{id}/suspend` | Suspend driver |
| `POST` | `/admin/drivers/{id}/reinstate` | Reinstate suspended driver |
| `PATCH` | `/admin/drivers/{id}/verify` | Verify driver identity |
| `PATCH` | `/admin/drivers/{id}/status` | Update approval status directly |

### Customers

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/customers` | List customers |
| `GET` | `/admin/customers/{id}` | Get customer |
| `PATCH` | `/admin/customers/{id}` | Update customer |
| `PATCH` | `/admin/customers/{id}/ban` | Ban customer |
| `POST` | `/admin/customers/{id}/suspend` | Suspend customer |
| `POST` | `/admin/customers/{id}/reinstate` | Reinstate |

### Rides

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/rides/live` | All active (non-terminal) rides |
| `GET` | `/admin/rides/live/{id}` | Live ride detail |
| `POST` | `/admin/rides/live/{id}/intervene` | Admin force-action on a live ride |
| `GET` | `/admin/rides` | Full ride history |
| `GET` | `/admin/rides/{id}` | Ride detail |

### Negotiation History

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/negotiations` | List all negotiation records |
| `GET` | `/admin/negotiations/{id}` | Detail |

### Pricing

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/pricing` | List all active pricing configs |
| `GET` | `/admin/pricing/{vehicle_type_code}` | Active config for vehicle type |
| `GET` | `/admin/pricing/{vehicle_type_code}/history` | Historical configs |
| `POST` | `/admin/pricing/{vehicle_type_code}` | Create new config (deactivates previous) |

**Create pricing config body:**
```json
{
  "base_fare_rwf": 500,
  "base_distance_km": 1.0,
  "tier1_per_km_rwf": 300,
  "tier1_max_km": 5.0,
  "tier2_per_km_rwf": 250,
  "night_surcharge_pct": 0.20,
  "night_start_hour": 22,
  "night_end_hour": 6,
  "waiting_rwf_per_min": 30.0,
  "waiting_free_minutes": 3,
  "min_fare_rwf": 500,
  "cancellation_fee_rwf": 200
}
```

### Revenue

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/revenue` | Revenue summary |
| `GET` | `/admin/revenue/kpis` | KPI metrics |
| `GET` | `/admin/revenue/transactions` | Transaction list |
| `POST` | `/admin/revenue/payouts/disburse` | Trigger driver payout disbursement |

### Analytics

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/analytics/overview` | Platform overview stats |
| `GET` | `/admin/analytics/rides/daily` | Daily ride counts |
| `GET` | `/admin/analytics/rides/weekly` | Weekly ride counts |
| `GET` | `/admin/analytics/revenue/breakdown` | Revenue breakdown |
| `GET` | `/admin/analytics/drivers/performance` | Per-driver metrics |
| `GET` | `/admin/analytics/negotiation/stats` | Negotiation funnel stats |
| `GET` | `/admin/analytics/heatmap` | Ride origin/destination heatmap |
| `GET` | `/admin/analytics/heatmap/zones` | Zone-level demand data |
| `GET` | `/admin/analytics/cancellations` | Cancellation analysis |
| `GET` | `/admin/analytics/funnel` | Ride conversion funnel |
| `GET` | `/admin/analytics/vehicle-mix` | Ride split by vehicle type |
| `GET` | `/admin/analytics/activity-heatmap` | Hour-of-day activity heatmap |
| `GET` | `/admin/analytics/satisfaction` | Rating satisfaction data |

### Safety Flags

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/flags/gps-anomalies` | GPS anomaly records |
| `GET` | `/admin/flags/device-collisions` | Device ID used on multiple accounts |

### Safety Incidents

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/incidents` | List incidents |
| `POST` | `/admin/incidents` | Create incident |
| `GET` | `/admin/incidents/{id}` | Get incident with event timeline |
| `PATCH` | `/admin/incidents/{id}/status` | Update status |
| `POST` | `/admin/incidents/{id}/acknowledge` | Acknowledge |
| `POST` | `/admin/incidents/{id}/escalate` | Escalate |
| `POST` | `/admin/incidents/{id}/resolve` | Resolve |
| `POST` | `/admin/incidents/{id}/message` | Append message to timeline |

### Support Tickets

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/support/tickets` | List tickets (filterable by status/priority) |
| `POST` | `/admin/support/tickets` | Create ticket |
| `GET` | `/admin/support/tickets/{id}` | Get ticket with thread |
| `POST` | `/admin/support/tickets/{id}/reply` | Add reply message |
| `POST` | `/admin/support/tickets/{id}/assign` | Assign to admin |
| `POST` | `/admin/support/tickets/{id}/resolve` | Resolve ticket |
| `PATCH` | `/admin/support/tickets/{id}` | Patch ticket fields |

### Inbox

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/inbox` | List inbox messages |
| `GET` | `/admin/inbox/{id}` | Get message |
| `POST` | `/admin/inbox/{id}/reply` | Reply to message |
| `PATCH` | `/admin/inbox/{id}` | Update status |
| `POST` | `/admin/inbox/{id}/archive` | Archive |
| `POST` | `/admin/inbox/{id}/spam` | Mark as spam |
| `DELETE` | `/admin/inbox/{id}` | Delete |

### Reports

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/reports` | List generated reports |
| `POST` | `/admin/reports/generate` | Generate report on demand |
| `GET` | `/admin/reports/scheduled` | List scheduled reports |
| `POST` | `/admin/reports/scheduled` | Create scheduled report |
| `POST` | `/admin/reports/scheduled/{id}/toggle` | Enable/disable scheduled report |
| `GET` | `/admin/reports/{id}` | Get report |
| `GET` | `/admin/reports/{id}/download` | Download report file |
| `DELETE` | `/admin/reports/{id}` | Delete report |

### Platform Settings

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/settings` | Get all platform settings |
| `PUT` | `/admin/settings/commission` | Update commission rates |
| `PUT` | `/admin/settings/negotiation` | Update negotiation rules |
| `PUT` | `/admin/settings/fares` | Update fare defaults |
| `PUT` | `/admin/settings/integrations` | Update integration keys |
| `PUT` | `/admin/settings/notifications` | Update notification settings |
| `POST` | `/admin/settings/regions` | Create service region |
| `PUT` / `PATCH` | `/admin/settings/regions/{id}` | Update region |
| `DELETE` | `/admin/settings/regions/{id}` | Delete region |

### Team / Admin Accounts

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/team` | List team members |
| `POST` | `/admin/team/invite` | Invite new admin |
| `GET` | `/admin/team/roles` | List roles with permissions |
| `POST` | `/admin/team/roles` | Create custom role |
| `PATCH` | `/admin/team/roles/{roleId}` | Update role |
| `DELETE` | `/admin/team/roles/{roleId}` | Delete role |
| `POST` | `/admin/team/members/{id}/role` | Update member's role |
| `POST` | `/admin/team/members/{id}/suspend` | Suspend member |
| `POST` | `/admin/team/members/{id}/reinstate` | Reinstate member |
| `POST` | `/admin/team/members/{id}/remove` | Remove from team |
| `POST` | `/admin/team/members/{id}/set-password` | Admin force-set password |

---

## Dev-Only Endpoints

These endpoints are only registered when `ENV != production`.

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/admin/dev/seed-drivers` | Seeds 100 realistic Kigali drivers across all vehicle types with GEO coordinates |

---

## Error Codes Reference

| Code | HTTP Status | Description |
|---|---|---|
| `UNAUTHORIZED` | 401 | Missing or invalid JWT |
| `FORBIDDEN` | 403 | Insufficient role |
| `NOT_FOUND` | 404 | Resource not found |
| `BAD_REQUEST` | 400 | Invalid request body |
| `RIDE_NOT_FOUND` | 404 | Ride does not exist |
| `INVALID_TRANSITION` | 422 | Ride status transition not allowed |
| `RIDE_CANCELLATION_NOT_ALLOWED` | 422 | Ride cannot be cancelled in current state |
| `ACCEPT_EXPIRED` | 410 | Driver accept TTL expired |
| `NO_CREDITS` | 402 | Driver has no ride credits |
| `INVALID_OTP` | 422 | OTP code is wrong or expired |
| `GEO_FENCE` | 422 | Driver not within required radius |
| `RATE_LIMIT` | 429 | Too many OTP requests |
