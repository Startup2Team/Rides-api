# Rides Intercity (Backend, Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the server side of scheduled multi-seat intercity trips — a driver publishes a route with seats and a price, passengers book seats against live availability, and the platform charges the driver credits per seat actually travelled.

**Architecture:** Two new tables (`intercity_trips`, `intercity_bookings`) plus a charge-obligation table, all additive. Seat inventory is guarded by a single conditional `UPDATE` backed by a CHECK constraint. Driver availability is gated by one denormalised timestamp column read on both matching paths. Credits are charged at departure via a durable obligation settled by an idempotent worker — never inside a request.

**Tech Stack:** Go, pgx/pgxpool, Postgres 16 + PostGIS, Redis, golang-migrate, chi, FCM, WebSockets.

**Spec:** `/Users/paccee/Pac/Rides/INTERCITY_DESIGN.md` (v3). Review history: `/Users/paccee/Pac/Rides/INTERCITY_REVIEW_FINDINGS.md`.

## Global Constraints

- **Soft delete only** — `deleted_at` on user-content tables, reads filter it, unique indexes partial on it. **Two deliberate exceptions:** `uq_intercity_booking_idem` (idempotency must outlive cancellation) and `intercity_credit_charges` (a financial obligation is immutable; a partial index would also make `ON CONFLICT` throw).
- **Never use `driver_ride_credits`** — orphaned legacy table. Credits are `driver_entitlements` + `ride_credit_ledger` via `internal/packages`.
- **Never use `vehicle_types.credit_cost_rwf`** — dead column, values invert per seat. Rate comes from config.
- **Balances are counts of rides, not RWF.**
- **Every migration ships a paired `.down.sql`.** `migrate.Up()` runs at server boot (`cmd/server/main.go:1736`); a failed migration leaves `schema_migrations` dirty and the API will not start.
- **Branch off fresh `origin/dev`.** Local `dev` is stale at 090, `origin/main` at 077, `origin/dev` at 095.
- **Additive API only** — no existing endpoint changes shape.
- **Money code is never self-reviewed.** Tasks 5-9 require `senior-security` + `senior-payments` review by an agent that did not write them.
- Verification: `go build ./... && go vet ./... && go test ./...` in `api-server`. DB tests: `go test -tags=integration ./test/dbit/...`.
- Known pre-existing failure: workflows/AuthContext AppState test (mobile). Ignore.

---

## File Structure

| File | Responsibility |
|---|---|
| `migrations/096_create_intercity.{up,down}.sql` | Corridors, trips, bookings |
| `migrations/097_intercity_credit_charges.{up,down}.sql` | Charge obligations + ledger source column |
| `migrations/098_intercity_profile_columns.{up,down}.sql` | `no_show_count`, `intercity_committed_until` |
| `internal/intercity/types.go` | Domain types, statuses, errors |
| `internal/intercity/repository.go` | All SQL. Seat acquisition and state transitions live here |
| `internal/intercity/service.go` | Business rules: caps, gates, tiering |
| `internal/intercity/handler.go` | HTTP, ownership predicates |
| `internal/intercity/departure_worker.go` | Creates charge obligations on the server clock |
| `internal/intercity/settlement_worker.go` | Drains obligations into the ledger |
| `internal/intercity/sweeper.go` | Hold expiry + stale-trip backstop |
| `internal/intercity/reconcile.go` | The seven alert-only sweeps |
| `internal/packages/ledger.go` (modify) | Extract `deductOneTx`; add `DeductForIntercityByProfile` |
| `internal/driver/repository.go:771` (modify) | One column comparison in `FindNearby` |
| `internal/matching/engine.go:~669` (modify) | One profile-field check |
| `internal/tracking/hub.go` (modify) | `RegisterTripWatcher` / `BroadcastToTrip` |

---

## Task 1: Schema foundation

**Files:**
- Create: `migrations/096_create_intercity.up.sql`, `migrations/096_create_intercity.down.sql`
- Create: `migrations/097_intercity_credit_charges.up.sql`, `.down.sql`
- Create: `migrations/098_intercity_profile_columns.up.sql`, `.down.sql`
- Test: `test/dbit/intercity_schema_test.go`

**Interfaces:**
- Produces: tables `intercity_corridors`, `intercity_trips`, `intercity_bookings`, `intercity_credit_charges`; columns `ride_credit_ledger.source_intercity_trip_id`, `customer_profiles.no_show_count`, `driver_profiles.intercity_committed_until`.

- [ ] **Step 1: Write the failing test**

```go
//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The CHECK constraint is the backstop that makes an oversell UNPERSISTABLE even
// if application logic is later wrong. If this test ever fails, seat integrity
// is gone regardless of what the Go code does.
func TestIntercityTrips_OversellIsUnpersistable(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6) // total_seats = 6

	_, err := pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = 7 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK seats_never_oversold must reject booked > total")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = 4, held_seats = 3 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK must reject booked + held > total")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = -1 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK seats_non_negative must reject negatives")
}

// A HELD booking with NULL hold_expires_at is invisible to the sweeper forever,
// because `hold_expires_at < NOW()` is NULL-false. The seats would be dead.
func TestIntercityBookings_HeldRequiresExpiry(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings (trip_id, customer_id, seats, status, price_per_seat_rwf)
		VALUES ($1, $2, 1, 'HELD', 3000)`, tripID, insertCustomer(t, ctx))
	require.Error(t, err, "CHECK hold_needs_expiry must reject HELD with NULL expiry")
}

// Idempotency must survive cancellation. If this index were partial on
// deleted_at, an offline client replaying its queue would consume seats twice.
func TestIntercityBookings_IdempotencySurvivesSoftDelete(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertCustomer(t, ctx)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, idempotency_key, deleted_at)
		VALUES ($1, $2, 1, 'CANCELLED', 3000, 'replay-key', NOW())`, tripID, cust)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, idempotency_key, hold_expires_at)
		VALUES ($1, $2, 1, 'HELD', 3000, 'replay-key', NOW() + interval '5 min')`, tripID, cust)
	require.Error(t, err, "idempotency key must still collide after soft delete")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./test/dbit/ -run TestIntercity -v`
Expected: FAIL — `relation "intercity_trips" does not exist`.

- [ ] **Step 3: Write the migrations**

Copy the DDL verbatim from spec §3 (migrations 096, 097, 098). Match repo style:
`IF NOT EXISTS` throughout, `idx_`/`uq_` prefixes, `TIMESTAMPTZ`, snake_case.
End `096.up.sql` with `ANALYZE intercity_trips;` and `098.up.sql` with
`ANALYZE driver_profiles;` — a new table/column has no stats and autovacuum may
not run before the first production `FindNearby`.

Each `.down.sql` drops in reverse dependency order. Note in `097.down.sql` that
dropping `source_intercity_trip_id` destroys audit linkage permanently.

Add the three helpers the tests need to `test/dbit/helpers_test.go`:
`insertIntercityTrip(t, ctx, seats int) string`, `insertCustomer(t, ctx) string`,
and seed one row into `intercity_corridors`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -tags=integration ./test/dbit/ -run TestIntercity -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Verify the down migrations actually work**

Run: `migrate -path migrations -database "$DATABASE_URL" down 3 && migrate -path migrations -database "$DATABASE_URL" up`
Expected: clean down and re-up, `schema_migrations` not dirty.

- [ ] **Step 6: Commit**

```bash
git add migrations/09{6,7,8}_*.sql test/dbit/intercity_schema_test.go test/dbit/helpers_test.go
git commit -m "feat(intercity): schema for trips, bookings and credit charges"
```

---

## Task 2: Seat acquisition (hold)

The one part of the design that survived four review rounds untouched. Do not
"improve" the statement.

**Files:**
- Create: `internal/intercity/types.go`, `internal/intercity/repository.go`
- Test: `test/dbit/intercity_seats_test.go`

**Interfaces:**
- Produces: `func (r *Repository) Hold(ctx context.Context, tripID, customerID string, seats int, idemKey string, sellable int) (*Booking, error)`; `ErrSeatsUnavailable`.

- [ ] **Step 1: Write the failing concurrency test**

```go
//go:build integration

package dbit

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The oversell race: N concurrent holds against a trip with fewer seats than
// they collectively ask for. Exactly total_seats worth may succeed. This is the
// test the whole seat design exists to pass.
func TestHold_ConcurrentHoldsNeverOversell(t *testing.T) {
	ctx := context.Background()
	const totalSeats = 6
	tripID := insertIntercityTrip(t, ctx, totalSeats)
	repo := intercity.NewRepository(pool)

	const workers = 20
	var wg sync.WaitGroup
	ok := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cust := insertCustomer(t, ctx)
			b, err := repo.Hold(ctx, tripID, cust, 2, fmt.Sprintf("k-%d", n), totalSeats)
			if err == nil {
				ok <- b.Seats
			}
		}(i)
	}
	wg.Wait()
	close(ok)

	granted := 0
	for n := range ok {
		granted += n
	}
	require.LessOrEqual(t, granted, totalSeats, "oversold: granted more seats than exist")

	var held, booked int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats, booked_seats FROM intercity_trips WHERE id=$1`, tripID).
		Scan(&held, &booked))
	require.Equal(t, granted, held+booked, "counters must match granted seats exactly")
}

// A client retry after a committed hold must NOT increment held_seats twice.
func TestHold_RetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertCustomer(t, ctx)
	repo := intercity.NewRepository(pool)

	first, err := repo.Hold(ctx, tripID, cust, 2, "same-key", 6)
	require.NoError(t, err)
	second, err := repo.Hold(ctx, tripID, cust, 2, "same-key", 6)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "retry must return the SAME booking")

	var held int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats FROM intercity_trips WHERE id=$1`, tripID).Scan(&held))
	require.Equal(t, 2, held, "retry must not double-increment held_seats")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -tags=integration ./test/dbit/ -run TestHold -v`
Expected: FAIL — `undefined: intercity.NewRepository`.

- [ ] **Step 3: Implement `Hold` — insert first, then the conditional UPDATE**

```go
// Hold reserves seats for ~5 minutes. Insert-first so a client retry cannot
// double-increment the counters: only a row that was actually inserted goes on
// to touch the trip. Locking order is ALWAYS booking -> trip; every other
// transition in this package must use the same order or we get ABBA deadlocks.
func (r *Repository) Hold(ctx context.Context, tripID, customerID string, seats int, idemKey string, sellable int) (*Booking, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var b Booking
	err = tx.QueryRow(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at, idempotency_key)
		SELECT $1, $2, $3, 'HELD', t.price_per_seat_rwf, NOW() + interval '5 minutes', $4
		  FROM intercity_trips t WHERE t.id = $1
		ON CONFLICT (customer_id, idempotency_key) WHERE idempotency_key IS NOT NULL
		DO NOTHING
		RETURNING id, seats`, tripID, customerID, seats, idemKey).Scan(&b.ID, &b.Seats)

	if errors.Is(err, pgx.ErrNoRows) {
		// Retry of a committed hold: return the existing booking, touch nothing.
		return r.findBookingByIdemKey(ctx, tx, customerID, idemKey)
	}
	if err != nil {
		return nil, err
	}

	var remaining int
	err = tx.QueryRow(ctx, `
		UPDATE intercity_trips
		   SET held_seats = held_seats + $2, updated_at = NOW()
		 WHERE id = $1
		   AND deleted_at IS NULL
		   AND status = 'OPEN'
		   AND depart_at > NOW() + interval '5 minutes'
		   AND booked_seats + held_seats + $2 <= total_seats
		   AND booked_seats + held_seats + $2 <= $3
		RETURNING total_seats - booked_seats - held_seats`, tripID, seats, sellable).Scan(&remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSeatsUnavailable // conflates six causes; classify for the message only
	}
	if err != nil {
		return nil, err
	}

	b.Remaining = remaining
	return &b, tx.Commit(ctx)
}
```

**Do not raise the transaction isolation level.** The statement is correct at
READ COMMITTED because Postgres re-evaluates the `WHERE` against the updated row
(EvalPlanQual). At REPEATABLE READ or SERIALIZABLE it raises 40001 instead.

- [ ] **Step 4: Run to verify it passes**

Run: `go test -tags=integration ./test/dbit/ -run TestHold -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/intercity/ test/dbit/intercity_seats_test.go
git commit -m "feat(intercity): oversell-safe seat acquisition with idempotent holds"
```

---

## Task 3: Confirm, cancel, and the hold sweeper

**Files:**
- Modify: `internal/intercity/repository.go`
- Create: `internal/intercity/sweeper.go`
- Test: `test/dbit/intercity_transitions_test.go`

**Interfaces:**
- Consumes: `Hold` from Task 2.
- Produces: `Confirm(ctx, bookingID, customerID) (*Booking, error)`, `CancelBooking(ctx, bookingID, customerID, role string) error`, `SweepExpiredHolds(ctx) (int, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// Confirm races the sweeper on an expiring hold. Without a conditional UPDATE on
// the BOOKING row, both act and held_seats is double-decremented.
func TestConfirm_RacesSweeperSafely(t *testing.T) { /* run both concurrently on an
   about-to-expire hold; assert exactly one wins and counters stay consistent */ }

// Two concurrent confirms must charge and move seats exactly once.
func TestConfirm_IsIdempotent(t *testing.T) { /* second call returns 200 with the
   same booking; booked_seats incremented once */ }

// The sweeper must only decrement for rows it actually transitioned.
func TestSweeper_OnlyReleasesRowsItTransitioned(t *testing.T) { /* ... */ }
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -tags=integration ./test/dbit/ -run "TestConfirm|TestSweeper" -v`
Expected: FAIL — undefined methods.

- [ ] **Step 3: Implement — every transition is conditional on the booking row first**

```go
// Zero rows means someone else already transitioned it. Return the current
// booking as an idempotent success; do NOT touch counters.
const confirmSQL = `
	UPDATE intercity_bookings SET status='CONFIRMED', updated_at=NOW()
	 WHERE id=$1 AND customer_id=$2 AND status='HELD'
	   AND hold_expires_at > NOW() AND deleted_at IS NULL
	RETURNING seats`

const sweepSQL = `
	UPDATE intercity_bookings SET status='EXPIRED', updated_at=NOW()
	 WHERE id IN (
	   SELECT id FROM intercity_bookings
	    WHERE status='HELD' AND hold_expires_at < NOW() AND deleted_at IS NULL
	    ORDER BY hold_expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)
	RETURNING trip_id, seats`
```

The sweeper runs behind a Redis `SETNX` lock so replica count is a non-question.
`FOR UPDATE SKIP LOCKED` mirrors `internal/admin/notifications.go:369`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -tags=integration ./test/dbit/ -run "TestConfirm|TestSweeper" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(intercity): conditional state transitions and hold sweeper"
```

---

## Task 4: The availability seam ⚠️ HIGHEST RISK

This edits **live city-ride dispatch** for a feature with no users. Two prior
design iterations got this wrong in opposite directions.

**Files:**
- Modify: `internal/driver/repository.go:771` (`FindNearby`), `internal/driver/types.go` (profile struct + `profileSelectCols`)
- Modify: `internal/matching/engine.go` (~line 669, after `FindProfileByID`)
- Modify: `internal/intercity/repository.go` (write/clear the column in the same tx as trip transitions)
- Test: `test/dbit/intercity_availability_test.go`

**Interfaces:**
- Consumes: `driver_profiles.intercity_committed_until` from Task 1.
- Produces: `Profile.IntercityCommittedUntil *time.Time`.

- [ ] **Step 1: Write the QA exit-path matrix as failing tests**

Every path that ends a commitment must restore availability. A miss here means a
driver is online, pinging, indexed — and silently skipped forever.

```go
func TestAvailability_RestoredOnEveryExitPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, ctx context.Context, tripID string)
	}{
		{"trip completes", func(...) { /* complete */ }},
		{"driver cancels", func(...) { /* cancel */ }},
		{"last passenger cancels", func(...) { /* cancel booking */ }},
		{"departs unfilled", func(...) { /* start with 0 bookings, complete */ }},
		{"expires unstarted", func(...) { /* stale-trip sweeper */ }},
		{"server restart mid-trip", func(...) { /* no in-memory state may be required */ }},
		{"driver completes a city ride while committed", func(...) {
			// ride/service.go:1399 sets DriverState=AVAILABLE unconditionally.
			// Intercity must NOT rely on DriverState, so this must not re-pool them.
		}},
		{"driver toggles offline->online while committed", func(...) {
			// driver/service.go:818 sets AVAILABLE when DriverActiveRide is empty,
			// and an intercity driver has no active ride.
		}},
		{"redis flushed mid-trip", func(...) {
			// Must become VISIBLE, never invisible. No Redis key is involved by design.
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { /* assert committed_until IS NULL or past */ })
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -tags=integration ./test/dbit/ -run TestAvailability -v`
Expected: FAIL — column not selected into `Profile`.

- [ ] **Step 3: Implement the two gates**

`driver/repository.go` `FindNearby`, added to the `WHERE`:
```sql
AND (dp.intercity_committed_until IS NULL OR dp.intercity_committed_until <= NOW())
```

`matching/engine.go`, immediately after the existing `FindProfileByID` call —
this path already loads the profile, so the check costs nothing extra:
```go
// Committed to a scheduled intercity trip. FindNearby carries the same predicate
// for the fallback path; both read the same authoritative column.
if profile.IntercityCommittedUntil != nil && profile.IntercityCommittedUntil.After(time.Now()) {
    continue
}
```

Add the column to `profileSelectCols` and `scanProfile`.

Write `intercity_committed_until` in the **same transaction** as every trip
status transition; clear it (NULL) on complete, cancel, and stale-sweep.

- [ ] **Step 4: Run to verify they pass**

Run: `go test -tags=integration ./test/dbit/ -run TestAvailability -v`
Expected: PASS (9 subtests).

- [ ] **Step 5: 🚧 HUMAN GATE — EXPLAIN on a staging clone**

No agent has DB access; this cannot be automated away.

```sql
EXPLAIN (ANALYZE, BUFFERS) <FindNearby query, with and without the new clause>
```
Run at 0, 500 and 50 000 `intercity_trips` rows against realistic `rides` and
`driver_locations` volumes. **Confirm the added predicate does not change the
plan shape and adds no measurable latency.** If it does, stop — the column can be
indexed or the check moved entirely into `engine.go`.

- [ ] **Step 6: Commit**

```bash
git commit -am "feat(intercity): gate committed drivers out of city dispatch on both paths"
```

**Deploy note for the release runbook:** migration first, then binary. On
rollback, **binary first, migration second** — rolling back 098 under a new
binary makes every `FindNearby` call error and kills the fallback dispatch path.

---

## Task 5: Extract `deductOneTx` without changing city-ride behaviour

**Files:**
- Modify: `internal/packages/ledger.go`
- Test: `test/dbit/ledger_sibling_test.go`

**Interfaces:**
- Produces: `deductOneTx(ctx, tx pgx.Tx, profileID, vehicleTypeID, entryType, idemKey string, sourceRideID, sourceTripID *string) (bool, error)`; `deductOne` keeps its exact signature and behaviour.

- [ ] **Step 1: Write the failing characterisation tests**

```go
// deductOne must remain byte-for-byte equivalent in behaviour: paid credits are
// spent BEFORE bonus, and the balance snapshot is written. ride/service.go:501
// depends on this and must not change.
func TestDeductOne_StillSpendsPaidFirst(t *testing.T) { /* grant 1 paid + 5 bonus;
   deduct; assert rides_delta = -1 and bonus untouched */ }

// The asymmetry is deliberate and load-bearing: collapsing deduct and refund into
// a generic adjust(+/-1) would make an intercity refund path trivial to add,
// which is the one thing this design eliminates by construction.
func TestRefundOne_CreditsPaidUnconditionally(t *testing.T) { /* ... */ }
```

- [ ] **Step 2: Run to verify they pass against current code**

Run: `go test -tags=integration ./test/dbit/ -run "TestDeductOne|TestRefundOne" -v`
Expected: PASS — these characterise *existing* behaviour before the refactor.

- [ ] **Step 3: Extract the body**

Move `deductOne`'s body verbatim into `deductOneTx`, taking the tx as a
parameter. `deductOne` becomes a thin wrapper that opens the tx and passes
`'RIDE_DEDUCTION', &rideID, nil`. Use `ON CONFLICT (idempotency_key) DO NOTHING`
plus `RowsAffected()` in the shared body — `deductOne` currently has no
`ON CONFLICT`, so two racing workers produce a raw `23505`.

Add a comment on `refundOne`: intercity has no refund path **by design**; do not
generalise these two functions.

- [ ] **Step 4: Run the full suite to verify nothing regressed**

Run: `go build ./... && go vet ./... && go test ./... && go test -tags=integration ./test/dbit/...`
Expected: PASS, including every existing city-ride test.

- [ ] **Step 5: Commit**

```bash
git commit -am "refactor(packages): extract deductOneTx, preserving deductOne exactly"
```

---

## Task 6: `DeductForIntercityByProfile`

**Files:**
- Modify: `internal/packages/ledger.go`
- Test: `test/dbit/intercity_ledger_test.go`

**Interfaces:**
- Consumes: `deductOneTx` from Task 5.
- Produces: `func (l *LedgerService) DeductForIntercityByProfile(ctx context.Context, profileID, vehicleTypeID, idemKey, tripID string) (bool, error)`.

Takes a **profile id and vehicle type id directly** — it must not call
`resolveProfile`, which re-derives the pool at settlement time from the driver's
*current* active vehicle. The trip's pool is frozen on the charge row.

- [ ] **Step 1: Write the failing test**

```go
// An idempotency hit returns (false, nil) — ALREADY CHARGED, not failed.
func TestDeductForIntercity_SecondCallIsNoOp(t *testing.T) {
	ok, err := svc.DeductForIntercityByProfile(ctx, pid, vtID, "intercity:t:1", tripID)
	require.NoError(t, err); require.True(t, ok)

	ok, err = svc.DeductForIntercityByProfile(ctx, pid, vtID, "intercity:t:1", tripID)
	require.NoError(t, err)
	require.False(t, ok, "(false, nil) means already charged")

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM ride_credit_ledger WHERE idempotency_key=$1`, "intercity:t:1").Scan(&n))
	require.Equal(t, 1, n, "exactly one ledger row")
}

func TestDeductForIntercity_WritesTripSourceNotRideSource(t *testing.T) { /* assert
   source_intercity_trip_id set, source_ride_id NULL, entry_type INTERCITY_DEDUCTION */ }
```

- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).

`entry_type='INTERCITY_DEDUCTION'` is 19 chars against `varchar(20)`. Do not
lengthen it without an ALTER.

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(packages): intercity credit deduction against the v4 ledger"
```

---

## Task 7: Departure worker — create the obligation

**Files:**
- Create: `internal/intercity/departure_worker.go`
- Test: `test/dbit/intercity_departure_test.go`

**Interfaces:**
- Produces: `CreateObligations(ctx, tripID string) (int, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// v1 charged per booking (1 credit for a 6-seat booking). v2 reintroduced the same
// bug by counting booking ROWS. Charge rows are per SEAT.
func TestObligations_ChargePerSeatNotPerBooking(t *testing.T) {
	// one 4-seat booking
	require.Equal(t, 4, mustCreateObligations(t, tripID), "4 seats => 4 charge rows")
}

// CONFIRMED and BOARDED are DISJOINT statuses. A partially-boarded manifest must
// still charge for every ever-confirmed seat.
func TestObligations_PartiallyBoardedChargesAllSeats(t *testing.T) {
	// 4 seats BOARDED + 2 seats CONFIRMED (driver never tapped Board)
	require.Equal(t, 6, mustCreateObligations(t, tripID))
}

// Not pressing /start must NOT be a free trip: the worker creates the obligation
// on the server's clock.
func TestObligations_CreatedWithoutStartBeingCalled(t *testing.T) { /* ... */ }

// Marks beyond the 40% budget are recorded but do not reduce the bill.
func TestObligations_NoShowBudgetCaps(t *testing.T) { /* 6 seats, all marked
   NO_SHOW => chargeable is floored at 60% */ }

func TestObligations_AreIdempotent(t *testing.T) { /* run twice => same row count */ }
```

- [ ] **Step 2-4:** run (FAIL) → implement per spec §11 D3 → run (PASS).

`chargeable` is the single `SUM(seats)` expression from the spec — never
`COUNT(*)`, never a `max()` of two partial views. `generate_series(1, chargeable)`
with `chargeable = 0` correctly inserts nothing.

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(intercity): create per-seat credit obligations at departure"
```

---

## Task 8: Settlement worker

**Files:**
- Create: `internal/intercity/settlement_worker.go`
- Test: `test/dbit/intercity_settlement_test.go`

**Interfaces:**
- Consumes: `DeductForIntercityByProfile` (Task 6), obligations (Task 7).

- [ ] **Step 1: Write the failing tests**

```go
// (false, nil) means ALREADY CHARGED. Treating it as failure would bill a driver
// and then penalise them for non-payment. ride/service.go:501 discards the bool
// entirely, so this is a real foot-gun.
func TestSettlement_IdempotencyHitMarksCharged(t *testing.T) { /* ... */ }

// Only ErrNoCredits may promote to ARREARS. Five transient errors must not write
// off a collectable debt.
func TestSettlement_TransientErrorsDoNotCauseArrears(t *testing.T) { /* ... */ }

// The claim must be COMMITTED before the money call. Holding it means two pooled
// connections per row, which can starve the pool and take down the whole API.
func TestSettlement_DoesNotHoldConnectionAcrossLedgerCall(t *testing.T) { /* run with a
   pool of size 2 and a backlog of 50 rows; must complete without deadlock */ }

func TestSettlement_CrashBetweenLedgerAndMarkConverges(t *testing.T) { /* simulate;
   retry must mark CHARGED without double-charging */ }
```

- [ ] **Step 2-4:** run (FAIL) → implement lease-and-release per spec §11 D3 → run (PASS).

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(intercity): idempotent credit settlement worker"
```

---

## Task 9: Arrears collection and visibility

**Files:** `internal/intercity/settlement_worker.go`, `internal/packages/ledger.go`, `internal/packages/admin_entitlements.go`
**Test:** `test/dbit/intercity_arrears_test.go`

- [ ] **Step 1:** failing tests — a grant flips `ARREARS` rows back to `PENDING`; `arrears_credits` appears on the entitlements response; `mapLedgerKind` labels `INTERCITY_DEDUCTION`; arrears promotion is **time-based**, not completion-based.
- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).
- [ ] **Step 5:** `git commit -am "feat(intercity): collect and surface credit arrears"`

**The balance must never go negative** — `HasCredits` gates going online, so a
negative balance would lock the driver out of city rides entirely.

---

## Task 10: Customer endpoints

**Files:** `internal/intercity/handler.go`, `internal/intercity/service.go`, `cmd/server/main.go` (route registration)
**Test:** `test/integration/intercity_customer_test.go`

- [ ] **Step 1:** failing tests — **ownership in the SQL predicate**, not a separate check: customer A cannot read, confirm or cancel customer B's booking (403/404, never 200); the 2-concurrent-booking cap; the per-booking seat cap; `sellable_seats` is **never** in any response body; `?date=` returns a 01:00 Kigali departure on its own date.
- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).
- [ ] **Step 5:** `git commit -am "feat(intercity): customer browse and booking endpoints"`

---

## Task 11: Driver endpoints

**Files:** `internal/intercity/handler.go`, `internal/intercity/service.go`
**Test:** `test/integration/intercity_driver_test.go`

- [ ] **Step 1:** failing tests — `RoleDriverActive` **only** (a `DRIVER_PENDING` applicant must be rejected); manifest PII tiering by time and status; `board`/`no-show` reject a `booking_id` belonging to another driver's trip; PATCH freeze engages on `booked_seats + held_seats > 0`; publish gate returns `402` with no credits; cancel requires a reason and is rejected once `IN_TRANSIT`.
- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).
- [ ] **Step 5:** `git commit -am "feat(intercity): driver publish, manifest and boarding endpoints"`

---

## Task 12: Realtime trip watchers

**Files:** `internal/tracking/hub.go`
**Test:** `test/integration/intercity_ws_test.go`

- [ ] **Step 1:** failing tests — `RegisterTripWatcher` **rejects** a user with no booking on that trip (otherwise any authenticated user gets a live feed of a rival's bookings); `trip_seats_changed` carries counts only, never passenger identities; the watcher is dropped when the booking ends.
- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).
- [ ] **Step 5:** `git commit -am "feat(intercity): authorised trip watchers over the existing hub"`

---

## Task 13: Reconciliation sweeps

**Files:** `internal/intercity/reconcile.go`
**Test:** `test/dbit/intercity_reconcile_test.go`

All seven sweeps from spec §12, **alert-only** via `pkg/alerting`. Sweep (d) —
`driver_entitlements` vs `SUM(ride_credit_ledger)` — is the one nothing covers
today and is mandatory, because this feature adds a second writer to the ledger.

- [ ] **Step 1:** failing tests — each sweep detects its drift class against a
  deliberately corrupted fixture, and reports rather than corrects.
- [ ] **Step 2-4:** run (FAIL) → implement → run (PASS).
- [ ] **Step 5:** `git commit -am "feat(intercity): reconciliation sweeps for seats and credits"`

---

## Review Gates

| After | Reviewer (must not be the author) |
|---|---|
| Task 1 | `senior-dba` |
| Task 4 | `senior-dba` (+ the human EXPLAIN gate) and `senior-qa` for the exit-path matrix |
| Tasks 5-9 | `senior-payments` **and** `senior-security` — money is never self-reviewed |
| Tasks 10-12 | `senior-security` (authz, PII tiering) |
| All | `code-reviewer` before merge |

## Self-Review

**Spec coverage:** §3→T1; §4→T2,T3; §5→T3; §6→T4; §7→T10,T11; §8→T12; §9→T11;
§11 D1→T10,T11; D2→T11; D3→T6,T7,T8,T9; D4→T11; §12→T13.
**Gap accepted:** the corridor seed data is a fixture in T1, not an admin CRUD —
Phase 1 is one corridor.
**Placeholders:** Tasks 3 and 9-13 carry test *intent* with exact assertions
rather than full bodies; every one names the precise behaviour and the file. The
high-risk tasks (1, 2, 4, 5, 6, 7, 8) carry real code.
**Type consistency:** `Hold`/`Confirm`/`CancelBooking`/`SweepExpiredHolds`,
`DeductForIntercityByProfile`, `CreateObligations`, `Profile.IntercityCommittedUntil`
are used consistently across tasks.
