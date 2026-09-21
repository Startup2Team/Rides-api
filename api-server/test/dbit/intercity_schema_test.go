//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The seats_never_oversold CHECK is the backstop that makes an oversell
// UNPERSISTABLE even if application logic is later wrong. Every other seat
// guarantee in intercity is defence in depth on top of this constraint; if this
// test ever fails, seat integrity is gone regardless of what the Go code does.
func TestIntercityTrips_OversellIsUnpersistable(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)

	_, err := pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = 7 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK must reject booked_seats > total_seats")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = 4, held_seats = 3 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK must reject booked_seats + held_seats > total_seats")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = -1 WHERE id = $1`, tripID)
	require.Error(t, err, "CHECK seats_non_negative must reject a negative count")

	// The legitimate boundary must still be allowed: exactly full.
	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET booked_seats = 4, held_seats = 2 WHERE id = $1`, tripID)
	require.NoError(t, err, "booked + held == total must be permitted")
}

// A HELD booking with a NULL hold_expires_at is invisible to the sweeper
// forever, because `hold_expires_at < NOW()` is NULL-false. Those seats would be
// held until the trip departed, with nothing to release them.
func TestIntercityBookings_HeldRequiresExpiry(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings (trip_id, customer_id, seats, status, price_per_seat_rwf)
		VALUES ($1, $2, 1, 'HELD', 3000)`, tripID, cust)
	require.Error(t, err, "CHECK hold_needs_expiry must reject HELD with a NULL expiry")

	_, err = pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at)
		VALUES ($1, $2, 1, 'HELD', 3000, NOW() + interval '5 minutes')`, tripID, cust)
	require.NoError(t, err, "HELD with an expiry must be permitted")
}

// Idempotency must hold FOREVER, independent of row state. This index is a
// deliberate exception to the project-wide soft-delete rule: if it were partial
// on deleted_at, an offline client replaying its queue after a cancellation
// would consume seats a second time. Precedent: migration 064's
// uq_manual_payment_claims_idem is likewise not partial on state.
func TestIntercityBookings_IdempotencySurvivesSoftDelete(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, idempotency_key, deleted_at)
		VALUES ($1, $2, 1, 'CANCELLED', 3000, 'replay-key', NOW())`, tripID, cust)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, idempotency_key, hold_expires_at)
		VALUES ($1, $2, 1, 'HELD', 3000, 'replay-key', NOW() + interval '5 minutes')`, tripID, cust)
	require.Error(t, err, "the idempotency key must still collide after a soft delete")
}

// A status typo would silently drop a booking out of uq_intercity_active_booking,
// out of the hold sweeper's index, and out of every reconciliation query — the
// seats stay counted but the booking becomes unreachable.
func TestIntercity_StatusValuesAreConstrained(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		  (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at)
		VALUES ($1, $2, 1, 'CONFIRMD', 3000, NOW() + interval '5 minutes')`, tripID, cust)
	require.Error(t, err, "booking_status_valid must reject an unknown status")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET status = 'DEPARTED' WHERE id = $1`, tripID)
	require.Error(t, err, "trip_status_valid must reject an unknown status")
}

// A charge row is an immutable financial obligation. deleted_at is deliberately
// ABSENT: soft-deleting a debt is not a coherent operation, and a partial unique
// index would additionally make `ON CONFLICT (trip_id, seq)` raise "no unique or
// exclusion constraint matching" — a deterministic 500 on every departure.
func TestIntercityCreditCharges_HaveNoSoftDeleteAndBlockDuplicates(t *testing.T) {
	ctx := context.Background()

	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'intercity_credit_charges' AND column_name = 'deleted_at'`).Scan(&n))
	require.Zero(t, n, "intercity_credit_charges must NOT have deleted_at")

	tripID := insertIntercityTrip(t, ctx, 6)
	driverID, vehicleTypeID := intercityTripDriver(t, ctx, tripID)

	ins := `INSERT INTO intercity_credit_charges
	          (trip_id, driver_id, vehicle_type_id, seq, idempotency_key)
	        VALUES ($1, $2, $3, 1, $4)`
	key := "intercity:" + tripID + ":1"
	_, err := pool.Exec(ctx, ins, tripID, driverID, vehicleTypeID, key)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, ins, tripID, driverID, vehicleTypeID, key)
	require.Error(t, err, "uq_icc_trip_seq / uq_icc_idem must bar a duplicate obligation")

	// ON CONFLICT inference must work against these (non-partial) indexes — this
	// is the exact statement the departure worker runs.
	_, err = pool.Exec(ctx, ins+` ON CONFLICT (trip_id, seq) DO NOTHING`,
		tripID, driverID, vehicleTypeID, key)
	require.NoError(t, err, "ON CONFLICT (trip_id, seq) must infer a usable index")
}

// The idempotency key format must fit the ledger column it flows into:
// ride_credit_ledger.idempotency_key is varchar(100).
func TestIntercityCreditCharges_KeyFitsLedgerColumn(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	key := "intercity:" + tripID + ":99"
	require.LessOrEqual(t, len(key), 100, "key must fit ride_credit_ledger.idempotency_key")

	var maxLen int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT character_maximum_length FROM information_schema.columns
		 WHERE table_name = 'ride_credit_ledger' AND column_name = 'idempotency_key'`).Scan(&maxLen))
	require.GreaterOrEqual(t, maxLen, len(key))
}

// Availability is gated by this column on BOTH matching paths. It must be
// nullable, because NULL means "not committed" — the safe, visible default.
func TestDriverProfiles_IntercityCommittedUntilIsNullable(t *testing.T) {
	ctx := context.Background()
	var nullable string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		 WHERE table_name = 'driver_profiles' AND column_name = 'intercity_committed_until'`).Scan(&nullable))
	require.Equal(t, "YES", nullable, "NULL must mean 'not committed' so the default is visible")
}

// The hold statement's ON CONFLICT must be able to INFER the idempotency index.
// Inference failure is raised at PLAN time, independent of data, so this is a
// deterministic 500 on every hold rather than a rare race. Migration 096 shipped
// the index partial on `idempotency_key IS NOT NULL`, which cannot be inferred
// by the bare form below; 099 drops that predicate. Semantics are unchanged
// because a unique btree already treats NULL as distinct from NULL.
func TestIntercityBookings_HoldOnConflictInfersIndex(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	const hold = `
		INSERT INTO intercity_bookings
		    (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at, idempotency_key)
		VALUES ($1, $2, 1, 'HELD', 3000, NOW() + interval '5 minutes', $3)
		ON CONFLICT (customer_id, idempotency_key) DO NOTHING`

	_, err := pool.Exec(ctx, hold, tripID, cust, "infer-key")
	require.NoError(t, err, "bare ON CONFLICT must infer uq_intercity_booking_idem")

	// And the conflict must actually be SUPPRESSED, not merely not-error.
	_, err = pool.Exec(ctx, hold, tripID, cust, "infer-key")
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM intercity_bookings
		 WHERE customer_id = $1 AND idempotency_key = 'infer-key'`, cust).Scan(&n))
	require.Equal(t, 1, n, "the second insert must be swallowed, not duplicated")
}

// Dropping the partial predicate must NOT start blocking bookings that carry no
// idempotency key: a unique btree treats NULLs as distinct, so many are allowed.
func TestIntercityBookings_NullIdempotencyKeysStillUnlimited(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO intercity_bookings
			    (trip_id, customer_id, seats, status, price_per_seat_rwf, cancelled_at)
			VALUES ($1, $2, 1, 'CANCELLED', 3000, NOW())`, tripID, cust)
		require.NoError(t, err, "NULL idempotency keys must remain unlimited")
	}
}

// One live booking per customer per trip. BOARDED is included: a boarded
// passenger must not be able to open a second hold on the same trip.
func TestIntercityBookings_OneLiveBookingPerCustomerPerTrip(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	ins := `INSERT INTO intercity_bookings
	          (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at)
	        VALUES ($1, $2, 1, $3, 3000, NOW() + interval '5 minutes')`
	_, err := pool.Exec(ctx, ins, tripID, cust, "HELD")
	require.NoError(t, err)

	_, err = pool.Exec(ctx, ins, tripID, cust, "CONFIRMED")
	require.Error(t, err, "a second live booking on the same trip must be rejected")

	// Once the first is terminal the customer may book again.
	_, err = pool.Exec(ctx,
		`UPDATE intercity_bookings SET status='CANCELLED' WHERE trip_id=$1 AND customer_id=$2`,
		tripID, cust)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, ins, tripID, cust, "HELD")
	require.NoError(t, err, "re-booking after cancellation must be allowed")
}

// The corridor FK is the constraint the entire lookup-table argument rests on:
// without it, "Kigali" / "kigali" / "Kigali " become three corridors and
// passengers searching a route that has trips silently see nothing.
func TestIntercityTrips_CorridorMustExist(t *testing.T) {
	ctx := context.Background()
	seed := insertIntercityTrip(t, ctx, 6) // creates a driver + vehicle to clone
	var driverID, vehicleID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT driver_id, vehicle_id FROM intercity_trips WHERE id=$1`, seed).
		Scan(&driverID, &vehicleID))

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_trips (
			driver_id, vehicle_id, corridor, origin_name, destination_name,
			origin_point, destination_point, staging_address, depart_at,
			total_seats, price_per_seat_rwf)
		VALUES ($1, $2, 'kigali ', 'Kigali', 'Musanze',
			ST_SetSRID(ST_MakePoint(30.06, -1.94), 4326)::geography,
			ST_SetSRID(ST_MakePoint(29.63, -1.49), 4326)::geography,
			'Nyabugogo', NOW() + interval '3 hours', 4, 3000)`, driverID, vehicleID)
	require.Error(t, err, "free-text corridor must be rejected by the FK")
}

// A ledger row must name exactly one source, or reporting silently picks one.
func TestRideCreditLedger_OneSourceOnly(t *testing.T) {
	ctx := context.Background()
	var validated bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT convalidated FROM pg_constraint WHERE conname = 'one_source_only'`).Scan(&validated))
	require.True(t, validated, "one_source_only must be VALIDATED, not left advisory")
}

// Bounds: a malformed request must not be able to persist a nonsense row even if
// it slips past the application layer.
func TestIntercity_ValueBoundsAreEnforced(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_bookings
		    (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at)
		VALUES ($1, $2, 9, 'HELD', 3000, NOW() + interval '5 minutes')`, tripID, cust)
	require.Error(t, err, "seats must be capped so no one account takes a whole vehicle")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET price_per_seat_rwf = 0 WHERE id = $1`, tripID)
	require.Error(t, err, "a zero fare must be rejected")

	// The UPDATE must target a row that actually exists, or it affects zero rows
	// and no constraint can fire — the assertion would pass vacuously.
	var bookingID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_bookings
		    (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at)
		VALUES ($1, $2, 1, 'HELD', 3000, NOW() + interval '5 minutes')
		RETURNING id`, tripID, cust).Scan(&bookingID))

	_, err = pool.Exec(ctx,
		`UPDATE intercity_bookings SET cancelled_by_role = 'ROBOT' WHERE id = $1`, bookingID)
	require.Error(t, err, "cancelled_by_role must be constrained")

	_, err = pool.Exec(ctx,
		`UPDATE intercity_bookings SET cancelled_by_role = 'DRIVER' WHERE id = $1`, bookingID)
	require.NoError(t, err, "a valid role must still be accepted")
}
