# Algorithms and Business Rules

## Matching Algorithm

Purpose: find the best available driver for a new ride.

Inputs:

- Ride ID.
- Pickup coordinate.
- Vehicle type.
- Matching config: radius, timeout, attempts.

Data sources:

- Redis GEO hot path: `drivers:geo:{vehicleType}`.
- PostGIS fallback: `driver_locations`.
- Driver profile data: acceptance rate, FCM token, status.
- Redis decline counters.

Steps:

1. Start matching asynchronously after ride creation.
2. Search candidate drivers by vehicle type near pickup.
3. Skip drivers already tried.
4. Validate Redis driver state is `AVAILABLE`.
5. Enrich candidate with profile and decline data.
6. Score candidates.
7. Sort lowest score first.
8. Offer ride to best candidate.
9. Lock candidate using Redis `SET NX`.
10. Store `ride:{rideID}:pending_driver`.
11. Send WebSocket ride request and optional FCM.
12. Wait for accept/decline or timeout.
13. On accept, assign driver and move ride to `NEGOTIATING`.
14. On decline/timeout, increment decline counter and try next candidate.
15. If exhausted, cancel ride as no-driver-found.

Score formula:

```text
score =
  normalized_distance * 0.60
  + normalized_declines * 0.25
  + acceptance_penalty * 0.15
```

Where:

```text
normalized_distance = distance_m / expanded_radius_m
normalized_declines = min(daily_declines, 10) / 10
acceptance_penalty = 1 - acceptance_rate / 100
```

## Negotiation Algorithm

Purpose: allow customer and driver to agree on fare.

Rules:

- Ride must be `NEGOTIATING`.
- Customer and driver each get at most 3 offers.
- Actor cannot accept their own latest proposal.
- Accepted fare is immutable through `fare_locked_at IS NULL`.
- Manual fare lock bypasses offer count.

Offer flow:

1. Validate ride status.
2. Count offers by actor role.
3. Reject if actor has used 3 offers.
4. Count total rounds.
5. Insert negotiation round.
6. Append ride event.
7. Publish analytics.
8. Broadcast WebSocket message to counterparty.

Accept flow:

1. Validate ride status.
2. Fetch latest round.
3. Reject if latest proposal was made by accepting role.
4. Set latest response to `ACCEPTED`.
5. Lock fare on ride.
6. Transition ride to `CONFIRMED`.
7. Broadcast `ride_confirmed`.

Manual fare lock:

1. Driver submits amount.
2. Validate ride status.
3. Lock fare.
4. Transition to `CONFIRMED`.
5. Broadcast `ride_confirmed` with `manual=true`.

## Ride State Machine

Allowed transitions:

```text
SEARCHING -> MATCHED
SEARCHING -> CANCELLED
MATCHED -> NEGOTIATING
MATCHED -> SEARCHING
NEGOTIATING -> CONFIRMED
NEGOTIATING -> SEARCHING
NEGOTIATING -> CANCELLED
CONFIRMED -> DRIVER_EN_ROUTE
DRIVER_EN_ROUTE -> DRIVER_ARRIVED
DRIVER_ARRIVED -> IN_PROGRESS
DRIVER_ARRIVED -> CANCELLED
IN_PROGRESS -> COMPLETED
```

Terminal states:

- `COMPLETED`
- `CANCELLED`

## Pickup Expiry Algorithm

Purpose: handle customer no-show after driver arrives.

Steps:

1. Driver calls `POST /driver/rides/{ride_id}/arrive`.
2. Backend geofence-checks pickup radius.
3. Ride moves to `DRIVER_ARRIVED`.
4. Backend writes `driver_arrived_at`.
5. Backend starts a timer.
6. If ride is still `DRIVER_ARRIVED` when timer fires:
   - Set `pickup_expired=true`.
   - Notify customer and driver.
7. Driver can call `POST /driver/rides/{ride_id}/cancel`.
8. Backend validates `pickup_expired=true`.
9. Ride is cancelled with reason `customer_no_show`.
10. Driver is released without decline penalty.

## Completion Algorithm

Purpose: complete ride, release driver, and record fare data.

Steps:

1. Driver calls `POST /driver/rides/{ride_id}/complete`.
2. If body includes `dest_lat` and `dest_lng`, update ride destination first.
3. Geofence-check driver against final destination.
4. Transition `IN_PROGRESS -> COMPLETED`.
5. Set `completed_at`.
6. Increment driver completed ride count.
7. If agreed fare exists, record fare into route cache.
8. Release driver Redis state.
9. Remove customer active ride and ride state keys.
10. Append event and publish analytics.
11. Notify customer over WebSocket.

## Route Cache Algorithm

Route key:

```text
origin_geohash6:dest_geohash6:vehicle_type
```

Get route:

1. Build route cache key.
2. Try Redis hot cache.
3. If Redis hit, return and asynchronously increment DB use count.
4. If Redis miss, query Postgres route cache.
5. Cache DB result in Redis for 24h.
6. Return route or nil.

Upsert route:

1. Client sends distance and duration.
2. Backend snaps origin/destination to geohash6.
3. Insert route cache row or increment use count.
4. Cache route result in Redis.

Record agreed fare:

1. Build route cache key.
2. Append fare to `agreed_fares` JSONB.
3. Recalculate `avg_fare_rwf`.
4. Invalidate Redis route cache key.

## GPS Plausibility Algorithm

Purpose: detect impossible driver movement.

Steps:

1. Driver sends location.
2. Backend validates coordinate ranges.
3. Backend reads last location from Redis history.
4. Compute distance and elapsed time.
5. Compute speed in km/h.
6. If speed exceeds configured max:
   - Insert GPS anomaly.
   - Increment Redis anomaly count.
   - Publish analytics.
   - Suspend after repeated anomalies.
   - Reject update.
7. Otherwise:
   - Store latest location in Redis.
   - Push to history list.
   - Update Redis GEO index if movement exceeds noise threshold.
   - Upsert PostGIS location.

## Driver Payout Algorithm

Current MVP payout:

```text
driver_payout = agreed_fare * 0.85
platform_fee = agreed_fare * 0.15
```

The driver earnings endpoints return payout, not gross fare.

## Customer Cancellation Algorithm

Rules:

- Customer can cancel only while ride is searching, matched, or negotiating.
- Each customer cancel increments daily cancel counter.
- Warning analytics event fires at configured threshold.
- Booking can be blocked after suspend threshold.

## Driver Decline Penalty Algorithm

Rules:

- Driver request decline increments daily decline counter.
- Priority demotion can occur after threshold.
- Auto-offline can occur after higher threshold.
- No-show cancellation after `pickup_expired=true` does not call this decline path.
