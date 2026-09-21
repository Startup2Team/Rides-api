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
