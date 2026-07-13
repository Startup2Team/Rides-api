# Rides API Reference (Mobile)

Complete reference of every endpoint the mobile apps (customer + driver) use:
the path, what you send, and what you get back. Generated from the Go source —
keep it in sync when handlers change.

---

## 1. Conventions

### Base URLs
| Env | REST | WebSocket |
|-----|------|-----------|
| Current dev (ngrok) | `https://undated-reunion-scowling.ngrok-free.dev/api/v1` | `wss://undated-reunion-scowling.ngrok-free.dev/api/v1` |
| Local | `http://localhost:8080/api/v1` | `ws://localhost:8080/api/v1` |

All paths below are **relative to the `/api/v1` prefix**.

### Required headers
| Header | When | Value |
|--------|------|-------|
| `Authorization` | All authenticated endpoints | `Bearer <access_token>` |
| `Content-Type` | Any request with a body | `application/json` |
| `ngrok-skip-browser-warning` | Only when hitting the ngrok URL | `true` |

### Response envelope
**Every** response is wrapped. Success:
```json
{ "data": { ... } }      // or { "data": [ ... ] }
```
Error:
```json
{ "error": { "code": "VALIDATION", "message": "human readable reason" } }
```
`204 No Content` responses have **no body** (success with nothing to return).

### Auth flow in one line
`register` (get OTP) → `verify-otp` (get tokens) → use `access_token` → on `401 TOKEN_EXPIRED` call `refresh` → on logout call `logout`.

- Access token lifetime: **15 min** (default). Refresh token: **30 days**.
- A revoked session (`401 TOKEN_REVOKED`) means the user was banned/logged out elsewhere — send them back to login.

### Enums
- **`transport_type`**: `MOTO_BIKE` · `CAB_TAXI` · `LIGHT_HILUX` · `HEAVY_FUSO` · `TUK_TUK`
- **`role_state`**: `CUSTOMER_ONLY` · `DRIVER_PENDING` · `DRIVER_ACTIVE`
- **Ride `status`**: `searching` → `matched` → `negotiating` → `confirmed` → `driver_en_route` → `driver_arrived` → `in_progress` → `completed` / `cancelled` (terminal). (Sent lowercase in `RideResponse`.)

---

## 2. Authentication — `/auth`

### `POST /auth/register`
Starts signup/login by sending an OTP. Rate-limited to 5/hour per phone.

**Request**
```json
{
  "phone_number": "+250791377973",   // required, E.164
  "full_name": "Jane Doe",            // optional, min 2 chars
  "email": "jane@x.com",              // optional
  "device_id": "uuid-from-device",    // required
  "platform": "ios"                   // required: "ios" | "android"
}
```
**Response** — dev: `200 { "data": { "dev_otp": "263838" } }` (OTP echoed for convenience). Prod: `204` (no body; OTP sent by SMS).

### `POST /auth/verify-otp`
Exchanges an OTP for tokens. Creates the user on first verify.

**Request**
```json
{
  "phone_number": "+250791377973",  // required
  "otp": "263838",                   // required, 6 digits
  "device_id": "uuid-from-device",   // required
  "platform": "ios",                 // optional
  "app_version": "1.0.0"             // optional
}
```
**Response** `200`
```json
{ "data": {
  "access_token": "jwt...",
  "refresh_token": "jwt...",
  "role_state": "CUSTOMER_ONLY",
  "user_id": "uuid"
} }
```
**Errors**: `INVALID_OTP`, `OTP_EXPIRED`, `ACCOUNT_SUSPENDED` (403).

### `POST /auth/refresh`
**Request** `{ "refresh_token": "jwt..." }`
**Response** `200 { "data": { "access_token", "refresh_token", "role_state" } }`

### `POST /auth/logout` 🔒
No body. **Response** `204`. (Also forces the user offline if they're a driver.)

---

## 3. Customer — `/customer` 🔒
Requires role `CUSTOMER_ONLY`, `DRIVER_ACTIVE`, or `DRIVER_PENDING`, and a non-suspended account.

### `GET /customer/profile`
**Response** `200`
```json
{ "data": {
  "id": "uuid", "phone_number": "+250...", "full_name": "Jane Doe",
  "email": "jane@x.com", "fcm_token": "…", "role_state": "CUSTOMER_ONLY"
} }
```

### `PUT /customer/profile`
**Request** (all optional) `{ "full_name": "…", "email": "…", "fcm_token": "…" }`
**Response** `204`

### `POST /customer/phone/change/request` — send OTP to a new number
**Request** `{ "new_phone": "+2507…" }` (E.164)
**Response** `204` (in non-prod: `200 { "data": { "dev_otp": "123456" } }`)
**Errors**: `SAME_PHONE` (400, already your number), `PHONE_TAKEN` (409, in use by another account). Rate-limited **5 / 10 min** per user.

### `POST /customer/phone/change/verify` — confirm + swap
**Request** `{ "new_phone": "+2507…", "otp": "123456" }`
**Response** `200 { "data": { "phone_number": "+2507…" } }` — `users.phone_number` is updated; existing JWTs stay valid (keyed on user_id).
**Errors**: `PHONE_TAKEN` (409, racing claim), `OTP_EXPIRED`/`INVALID_OTP`/`OTP_LOCKED` on a bad code.

### `GET /customer/level` — loyalty / gamification
Loyalty tier derived from lifetime **COMPLETED** rides (the reliable on-platform signal — fares are paid off-app). Tiers: `BRONZE` (0), `SILVER` (10), `GOLD` (50), `PREMIUM` (150 rides).
**Response** `200`
```json
{ "data": {
  "level": "GOLD", "level_index": 2, "completed_rides": 55, "total_spend": 42000,
  "current_threshold": 50, "next_level": "PREMIUM", "next_threshold": 150,
  "rides_to_next_level": 95, "progress_to_next": 0.05,
  "perks": ["Faster support responses", "Early access to new features"]
} }
```
At `PREMIUM` (top tier): `next_level`/`next_threshold` are `null`, `progress_to_next` is `1`.

### `POST /customer/location` — nearby drivers
**Request**
```json
{ "lat": -1.9441, "lng": 30.0619, "transport_type": "MOTO_BIKE" }   // transport_type optional ("" = all)
```
**Response** `200`
```json
{ "data": { "drivers": [
  { "transport_type": "MOTO_BIKE", "distance_m": 320.5, "approx_lat": -1.944, "approx_lng": 30.061, "eta_minutes": 2 }
] } }
```
> Driver positions are **anonymised/approximate** (no driver id) until a ride is matched.

### `GET /customer/fare-estimate`
**Query**: `transport_type` (required), `pickup_lat`, `pickup_lng`, `dest_lat`, `dest_lng` (all required floats).
**Response** `200`
```json
{ "data": {
  "transport_type": "MOTO_BIKE",
  "distance_km": 5.2, "duration_minutes": 14,
  "breakdown": { /* fare component breakdown */ },
  "min_fare_rwf": 600, "night_surcharge_pct": 0.2,
  "night_start_hour": 22, "night_end_hour": 5,
  "waiting_rwf_per_min": 10, "waiting_free_minutes": 3,
  "cancellation_fee_rwf": 500,
  "note": "Night surcharge of 20% applies after 22:00"
} }
```
**Errors**: `ROUTE_NOT_FOUND` (404), `PRICING_NOT_FOUND` (404).

### `POST /customer/rides` — request a ride
**Request**
```json
{
  "pickup_lat": -1.9441, "pickup_lng": 30.0619, "pickup_address": "Kigali Heights",
  "dest_lat": -1.9550,  "dest_lng": 30.0940,  "dest_address": "Kimironko Market",
  "transport_type": "MOTO_BIKE",   // required
  "initial_fare": 1500,            // optional, customer's opening offer (RWF)
  "distance_km": 5.2               // optional, client-measured distance
}
```
**Response** `201 { "data": { "ride_id": "uuid", "status": "searching" } }`
**Errors**: `RIDE_ALREADY_ACTIVE` (409), `CUSTOMER_SUSPENDED` (403).
> After this, **open the customer WebSocket** (§7) to receive `driver_matched` / negotiation / live tracking.

### `GET /customer/rides` — ride history
**Query**: `limit` (default 20, max 100), `offset` (default 0).
**Response** `200 { "data": { "rides": [ RideResponse, … ], "limit": 20, "offset": 0 } }`

### `GET /customer/rides/active`
Current non-terminal ride (for app-restart recovery). **Response** `200 { "data": RideResponse }`, or `404 NOT_FOUND` if none.

### `GET /customer/rides/{ride_id}`
**Response** `200 { "data": RideResponse }`

### `DELETE /customer/rides/{ride_id}` — cancel
**Request** (optional) `{ "reason": "changed my mind" }`
**Response** `204`
> ⚠️ Counts toward the cancellation-penalty ladder (warn at 4/day, 24h ban at 5/day).

### Negotiation (customer side)
| Method | Path | Body | Response |
|--------|------|------|----------|
| `POST` | `/customer/rides/{ride_id}/negotiation/propose` | `{ "amount": 1500 }` | `204` |
| `POST` | `/customer/rides/{ride_id}/negotiation/accept` | — | `204` |
| `POST` | `/customer/rides/{ride_id}/negotiation/decline` | — | `204` |

**Errors**: `NEGOTIATION_ROUND_LIMIT` (409), `FARE_LOCKED` (409). Outcomes arrive over WebSocket (`negotiation_message`, `ride_confirmed`, `negotiation_declined`).

---

## 4. Driver — `/driver` 🔒

`/driver/apply` only needs a logged-in account. Everything under "profile" needs role `DRIVER_PENDING`/`DRIVER_ACTIVE`; ride/earnings/location/packages need `DRIVER_ACTIVE`.

### `POST /driver/apply` — submit driver application
**Request**
```json
{
  "transport_type": "MOTO_BIKE",        // required
  "vehicle_plate": "RAD 123 A",         // required
  "license_number": "DL-000123",        // required
  "date_of_birth": "1995-04-12",        // required, YYYY-MM-DD
  "city": "Kigali",                      // required
  "momo_pay_code": "123456",            // required
  "momo_provider": "mtn",               // required: "mtn" | "airtel"
  "province": "Kigali", "district": "Gasabo", "sector": "Remera",
  "cell": "Rukiri II", "village": "Amahoro",   // all required
  "passenger_seats": 1,                  // optional
  "load_capacity_kg": 0                  // optional
}
```
**Response** `201 { "data": DriverProfile }`. **Errors**: `DRIVER_ALREADY_APPLIED` (409), `INVALID_DOB` (400).

### `GET /driver/profile`
**Response** `200 { "data": DriverProfile }` — see [DriverProfile](#driverprofile) below.

### `PUT /driver/profile`
**Request** (all optional) `{ "city", "momo_pay_code", "momo_provider", "fcm_token" }`
**Response** `204`

### `POST /driver/policy/accept`
No body. **Response** `204`.

### `POST /driver/documents`
**Request**
```json
{ "document_type": "LICENCE_FRONT", "file_url": "https://cdn/.../x.jpg" }
```
`document_type`: `LICENCE_FRONT` · `VEHICLE_INSURANCE` · `VEHICLE_AUTHORIZATION`. **Response** `204`.

### `GET /driver/documents`
**Response** `200 { "data": { "documents": [ { "id", "document_type", "file_url", "uploaded_at" } ] } }`

### `GET /driver/session` — one-call bootstrap
Profile + active vehicle + ride flag + document-expiry alerts, for app startup/reconnect.
**Response** `200`
```json
{ "data": {
  "profile": { "…driver profile incl. approval_status, is_online, expiry dates…" },
  "active_vehicle": { "id": "uuid", "vehicle_type_code": "MOTO_BIKE", "plate_number": "RA123B", "is_active": true },
  "vehicle_count": 2,
  "has_active_ride": false,
  "document_alerts": [
    { "document": "insurance", "expires_on": "2026-07-20", "days_left": 11, "status": "EXPIRING_SOON" }
  ]
} }
```

### Vehicles — multi-vehicle + switching
| Method | Path | Notes |
|--------|------|-------|
| `GET` | `/driver/vehicles` | Own vehicles, active first. Legacy profiles are lazily backfilled. |
| `POST` | `/driver/vehicles` | `{vehicle_type_code, plate_number, …}` — first one auto-activates. `409 DUPLICATE_PLATE`. |
| `PATCH` | `/driver/vehicles/{id}` | Partial update. |
| `DELETE` | `/driver/vehicles/{id}` | `409 LAST_VEHICLE` if it's the only one; deleting the active one activates the oldest remaining. |
| `POST` | `/driver/vehicles/{id}/activate` | Switch. Syncs `transport_type` for matching in one transaction. **`403 DRIVER_NOT_APPROVED`**, **`409 VEHICLE_SWITCH_ON_RIDE`** during an active ride. |

> Daily job: drivers get a push + in-app notification when license/insurance/authorization expires within 30 days (day marks 30/14/7/3/1/0, once after expiry).

### `POST /driver/availability` — go online/offline
**Request** `{ "is_online": true }`
**Response** `204`. **Error**: `DRIVER_OFFLINE_COOLDOWN` (403) if toggling too fast.

### `POST /driver/location` — push GPS (REST fallback)
Rate-limited 20/min. Prefer the **WebSocket** `location_update` while online.
**Request**
```json
{ "lat": -1.9441, "lng": 30.0619, "speed_kmh": 24.5, "heading": 180.0 }  // speed_kmh, heading optional
```
**Response** `204`. **Errors**: `GPS_PLAUSIBILITY` (422), `GPS_INVALID_COORDS` (400).

### `GET /driver/demand-heatmap` — where riders are requesting
Bucketed recent ride-pickup demand (~110 m grid) so a driver can reposition. Rate-limited 30/min.
**Query** (all optional): `lat` + `lng` (a valid **pair** scopes to `radius_km` around the point via PostGIS `ST_DWithin`; omit both for busiest cells platform-wide — a lone/invalid coordinate is a `400 VALIDATION`), `window_min` (default 120, clamped 15–1440), `radius_km` (default 5, clamped 0.5–50).
**Response** `200`
```json
{ "data": {
  "window_minutes": 120, "radius_meters": 5000, "scoped": true,
  "points": [ { "lat": -1.944, "lng": 30.061, "count": 12 } ]
} }
```

### Packages & credits
| Method | Path | Body / Query | Response |
|--------|------|--------------|----------|
| `GET` | `/driver/packages?vehicle_type=MOTO_BIKE` | `vehicle_type` query (required) | `200 { "data": [ Package ] }` |
| `POST` | `/driver/packages/purchase` | `{ "package_id": "uuid" }` | `201 { "data": { "credit": {…}, "bonus": {…}\|null } }` |
| `GET` | `/driver/credits` | — | `200 { "data": { "credit": {…} } }` |
| `GET` | `/driver/bonuses` | — | `200 { "data": { "grants": [ … ] } }` |
| `GET` | `/driver/bonuses/tiers` | — | `200 { "data": { "tiers": [ … ] } }` |

Purchase deducts the price from the driver's wallet. **Error**: `NO_CREDITS` (402) when accepting a ride with no credits.

### Driver ride lifecycle
| Method | Path | Body | Response | Notes |
|--------|------|------|----------|-------|
| `GET` | `/driver/rides/active` | — | `200 { "data": RideResponse }` | restart recovery |
| `GET` | `/driver/rides/{ride_id}` | — | `200 { "data": RideResponse }` | includes `customer_name`/`customer_phone` |
| `POST` | `/driver/rides/{ride_id}/accept` | — | `204` | `ACCEPT_EXPIRED` (409), `NO_CREDITS` (402) |
| `POST` | `/driver/rides/{ride_id}/decline` | — | `204` | |
| `POST` | `/driver/rides/{ride_id}/en-route` | — | `204` | confirmed → driver_en_route |
| `POST` | `/driver/rides/{ride_id}/arrive` | — | `204` | → driver_arrived (geofenced) |
| `POST` | `/driver/rides/{ride_id}/start` | — | `204` | → in_progress (geofenced; charges credit) |
| `POST` | `/driver/rides/{ride_id}/complete` | `{ "dest_lat", "dest_lng", "dest_address" }` (all optional, but lat+lng together) | `204` | → completed |
| `POST` | `/driver/rides/{ride_id}/cancel` | `{ "reason": "…" }` (optional) | `204` | ⚠️ penalty ladder (warn 3/day, ban 4/day) |

**Common ride errors**: `INVALID_TRANSITION` (409), `GEO_FENCE_VIOLATION` (422), `RIDE_NOT_FOUND` (404).

### Negotiation (driver side)
| Method | Path | Body | Response |
|--------|------|------|----------|
| `POST` | `/driver/rides/{ride_id}/negotiation/propose` | `{ "amount": 1800 }` | `204` |
| `POST` | `/driver/rides/{ride_id}/negotiation/accept` | — | `204` |
| `POST` | `/driver/rides/{ride_id}/negotiation/decline` | — | `204` |
| `POST` | `/driver/rides/{ride_id}/negotiation/lock-fare` | `{ "amount": 1800 }` | `204` |
| `POST` | `/driver/rides/{ride_id}/negotiation/initiate-call` | — | `200 { "data": { "masked_number": "+250…" } }` |

### Earnings & stats
| Method | Path | Response |
|--------|------|----------|
| `GET` | `/driver/earnings/daily` | `200 { "data": { "total_rwf": 12000, "period": "today" } }` |
| `GET` | `/driver/earnings/weekly` | `200 { "data": { "total_rwf": 54000, "period": "last_7_days" } }` |
| `GET` | `/driver/stats` | `200 { "data": { "total_rides": 312, "acceptance_rate": 0.92, "completion_rate": 0.97, "priority_tier": 2 } }` |

---

## 5. Shared (any authenticated user) 🔒

### Mode switching — `PATCH /users/mode`
**Request** `{ "mode": "driver" }` (`"customer"` | `"driver"`). **Response** `204`.

### Saved locations — `/users/me/saved-locations`
| Method | Path | Body | Response |
|--------|------|------|----------|
| `GET` | `/users/me/saved-locations` | — | `200 { "data": { "saved_locations": [ … ] } }` |
| `POST` | `/users/me/saved-locations` | `{ "label", "address", "lat", "lng" }` | `201 { "data": SavedLocation }` |
| `PUT` | `/users/me/saved-locations/{id}` | `{ "label", "address", "lat", "lng" }` | `204` |
| `DELETE` | `/users/me/saved-locations/{id}` | — | `204` |

### Wallet — `/wallet`
| Method | Path | Body / Query | Response |
|--------|------|--------------|----------|
| `GET` | `/wallet` | — | `200 { "data": { "id", "user_id", "balance_rwf", "created_at", "updated_at" } }` |
| `GET` | `/wallet/transactions?limit=20&offset=0` | paging query | `200 { "data": { "transactions": [ Transaction ], "limit", "offset" } }` |
| `POST` | `/wallet/top-up` | `{ "amount_rwf": 5000, "phone_number": "078…" }` | `201 { "data": Transaction }` |
| `POST` | `/wallet/withdraw` | `{ "amount_rwf": 5000, "phone_number": "078…" }` | `200 { "data": Transaction }` |

`Transaction`: `{ "id","wallet_id","user_id","type","amount_rwf","balance_after","description","phone_number","external_ref","status","created_at" }`.

### Uploads — `POST /uploads/presigned-url`
Get a one-time S3/R2 URL to PUT a file to, then send the returned `file_url` to `/driver/documents`.
**Request** `{ "content_type": "image/jpeg", "purpose": "driver_document" }`
(`content_type`: `image/jpeg` · `image/png` · `image/heic` · `application/pdf`)
**Response** `200`
```json
{ "data": {
  "upload_url": "https://…(PUT here with the same Content-Type)…",
  "file_url": "https://cdn/…/documents/abc.jpg",
  "expires_in": 300,
  "max_size": 10485760
} }
```

### Locations — `/locations`
| Method | Path | Auth | Body / Query | Response |
|--------|------|------|--------------|----------|
| `GET` | `/locations/landmarks` | public | — | `200 { "data": { "landmarks": [ … ] } }` |
| `GET` | `/locations/suggestions` | 🔒 | — | `200 { "data": { … } }` (recent/frequent places) |
| `GET` | `/locations/route` | 🔒 | `pickup_lat,pickup_lng,dest_lat,dest_lng,vehicle_type` | `200 { "data": { "route": {…} } }` |
| `POST` | `/locations/route` | 🔒 | `{ "pickup_lat","pickup_lng","dest_lat","dest_lng","vehicle_type","distance_km","duration_minutes" }` | `200 { "data": { "route": {…} } }` |

### `GET /rides/active` 🔒
Generic active-ride lookup for the logged-in user (reconnect recovery). `200 { "data": ride }` or `404`.

---

## 6. Public (no auth)
| Method | Path | Body | Response |
|--------|------|------|----------|
| `GET` | `/pricing` | — | `200 { "data": { "vehicle_types": [ … ] } }` |
| `POST` | `/contact` | `{ "name","email","phone","category","subject","message" }` | submits a contact/support message |
| `GET` | `/health` (no `/api/v1` prefix) | — | `200 { "data": { "status": "ok" } }` |

---

## 7. WebSockets

Real-time ride flow runs over WS. **Auth: pass the access token as a query param** (`?token=…`) — mobile can't set headers on a socket.

| Socket | URL | Who | Direction |
|--------|-----|-----|-----------|
| Driver | `wss://…/api/v1/ws/driver?token=<access_token>` | online drivers (needs active driver profile) | bi-directional |
| Customer | `wss://…/api/v1/ws/customer?token=<access_token>&ride_id=<ride_id>` | a customer with a matched ride | server → client only |

- Server pings every ~54s; reply with pong (most WS libs auto-pong). Read timeout is 60s.
- **On (re)connect** both sockets immediately push a `ride_state` message so the app can jump to the right screen after a background/kill.

### Message shape (all messages)
```json
{ "type": "driver_location", "ride_id": "uuid", "payload": { … } }
```

### Client → server (driver socket only)
```json
{ "type": "location_update", "lat": -1.9441, "lng": 30.0619, "speed_kmh": 24.5, "heading": 180 }
```
Rejected updates come back as `{ "type": "error", "payload": { "message": "…" } }` (e.g. GPS implausible).

### Server → client message catalog
| `type` | Sent to | `payload` fields |
|--------|---------|------------------|
| `ride_state` | both | `status`, `ride_id`; customer also gets `driver_lat`, `driver_lng` if known |
| `ride_request` | driver | `ride_id`, `distance_m`, `transport_type`, `distance_km`, `pickup_lat/lng`, `pickup_address`, `dest_lat/lng`, `dest_address`, `suggested_fare`, `customer_name`, `customer_phone` |
| `driver_matched` | customer | `driver_id`, `distance_m`, `driver_name`, `driver_phone`, `vehicle_plate`, `transport_type`, `lat`, `lng` |
| `negotiation_message` | other party | `round_id`, `amount`, `proposed_by` (`"CUSTOMER"`\|`"DRIVER"`) |
| `negotiation_declined` | other party | (round/decline info) |
| `negotiation_call_prompt` | both | prompt to place the free `tel:` call |
| `ride_confirmed` | both | fare agreed; ride → `confirmed` |
| `driver_en_route` | customer | — |
| `driver_arrived` | customer | — |
| `ride_started` | customer | ride → `in_progress` |
| `driver_location` | customer | `lat`, `lng` (EMA-smoothed live position) |
| `ride_pickup_expired` | both | pickup wait window expired |
| `ride_completed` | both | ride finished |
| `ride_cancelled` | both | ride cancelled (by either party / system) |
| `error` | driver | `message` |

---

## 8. Reference objects

### RideResponse
Returned by every ride GET/list endpoint.
```json
{
  "id": "uuid",
  "customer_id": "uuid",
  "customer_name": "Jane Doe",         // present on driver-facing reads
  "customer_phone": "+250…",           // present on driver-facing reads
  "driver_id": "uuid | null",
  "transport_type": "MOTO_BIKE",
  "status": "in_progress",
  "pickup_lat": -1.9441, "pickup_lng": 30.0619, "pickup_address": "…",
  "dest_lat": -1.9550, "dest_lng": 30.0940, "destination_address": "…",
  "estimated_distance_km": 5.2,
  "customer_initial_fare": 1500,        // opening offer
  "agreed_fare": 1800,                  // null until negotiation locks
  "estimated_fare_rwf": 1750,
  "night_surcharge_applied": false, "night_surcharge_pct": 0.0,
  "waiting_seconds": 0, "waiting_charge_rwf": 0,
  "cancellation_fee_rwf": 0,
  "final_fare_rwf": null,               // set on completion
  "cancel_reason": null,
  "driver_arrived_at": null, "started_at": null, "completed_at": null,
  "pickup_expired": false,
  "created_at": "2026-06-13T09:00:00Z",
  "updated_at": "2026-06-13T09:05:00Z"
}
```
All `*_at`, fare, and `driver_id` fields are nullable.

### DriverProfile
```json
{
  "id": "uuid", "user_id": "uuid",
  "transport_type": "MOTO_BIKE", "vehicle_plate": "RAD 123 A",
  "license_number": "…", "date_of_birth": "1995-04-12T00:00:00Z",
  "city": "Kigali", "momo_pay_code": "123456", "momo_provider": "mtn",
  "province": "…", "district": "…", "sector": "…", "cell": "…", "village": "…",
  "passenger_seats": 1, "load_capacity_kg": 0,
  "approval_status": "approved",      // pending | approved | rejected | suspended
  "approved_by": null, "approved_at": null,
  "rejection_reason": null, "suspension_reason": null,
  "is_online": true, "priority_tier": 2, "offline_at": null,
  "acceptance_rate": 0.92, "total_rides": 312,
  "policy_accepted": true, "fcm_token": "…",
  "created_at": "…", "updated_at": "…"
}
```

### Error codes
| HTTP | `code` | Meaning |
|------|--------|---------|
| 400 | `BAD_REQUEST` / `VALIDATION` | malformed or invalid body |
| 400 | `INVALID_OTP` / `OTP_EXPIRED` / `OTP_ALREADY_USED` | OTP problems |
| 400 | `GPS_INVALID_COORDS` | lat/lng out of range |
| 401 | `UNAUTHORIZED` | missing/invalid token |
| 401 | `TOKEN_EXPIRED` | refresh and retry |
| 401 | `TOKEN_REVOKED` | session killed (banned/logout) → re-login |
| 401 | `TOKEN_INVALID` | bad token |
| 402 | `NO_CREDITS` | driver must buy a package |
| 403 | `FORBIDDEN` | wrong role |
| 403 | `ACCOUNT_SUSPENDED` / `CUSTOMER_SUSPENDED` | account/booking suspended |
| 403 | `DRIVER_NOT_ACTIVE` / `DRIVER_OFFLINE_COOLDOWN` | driver gating |
| 404 | `NOT_FOUND` / `RIDE_NOT_FOUND` / `ROUTE_NOT_FOUND` / `PRICING_NOT_FOUND` | missing resource |
| 409 | `CONFLICT` | duplicate |
| 409 | `INVALID_TRANSITION` | illegal ride state change |
| 409 | `FARE_LOCKED` | fare already agreed |
| 409 | `ACCEPT_EXPIRED` | ride request TTL passed |
| 409 | `RIDE_ALREADY_ACTIVE` | one active ride at a time |
| 409 | `NEGOTIATION_ROUND_LIMIT` | max rounds reached |
| 409 | `DRIVER_ALREADY_APPLIED` | application exists |
| 422 | `GEO_FENCE_VIOLATION` | driver not within required radius |
| 422 | `GPS_PLAUSIBILITY` | GPS jump too fast — rejected |
| 429 | `RATE_LIMITED` | slow down |
| 500 | `INTERNAL` | server error |

---

## 9. Admin endpoints (web panel — not for mobile)
The `/admin/**` tree (auth/2FA, dashboard, drivers, customers, users, flags, live rides,
rides, pricing, negotiations, revenue, analytics, incidents, support tickets, inbox,
reports, bonuses, packages, settings, team) powers the **admin web frontend** and is not
used by the mobile apps. They require an `ADMIN` role token. Ask the backend team for
the separate admin reference if you need it.
