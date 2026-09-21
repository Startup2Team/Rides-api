package intercity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Every state transition in this file has the same shape, and it is not an
// accident:
//
//  1. ONE conditional UPDATE on the BOOKING row, asserting the state it is
//     moving from. Zero rows means someone else already moved it — a racing
//     sweeper, a retried request, a concurrent cancel.
//  2. Only the transaction that actually moved the row touches the trip
//     counters.
//
// Without (1), confirm and the hold sweeper can both act on the same expiring
// booking and decrement held_seats twice: seats_non_negative then aborts one of
// them, surfacing as a 500 to a real passenger, and the sweeper would keep
// crashing on the same batch every cycle.
//
// Lock order is ALWAYS booking row then trip row, matching Hold. Reversing it
// anywhere in this package deadlocks against these paths.

const confirmBookingSQL = `
	UPDATE intercity_bookings
	   SET status = 'CONFIRMED', updated_at = NOW()
	 WHERE id = $1
	   AND customer_id = $2
	   AND status = 'HELD'
	   AND hold_expires_at > NOW()
	   AND deleted_at IS NULL
	RETURNING seats`

const moveHeldToBookedSQL = `
	UPDATE intercity_trips
	   SET held_seats = held_seats - $2, booked_seats = booked_seats + $2, updated_at = NOW()
	 WHERE id = $1`

const loadBookingForCustomerSQL = `
	SELECT id, trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at
	  FROM intercity_bookings
	 WHERE id = $1 AND customer_id = $2 AND deleted_at IS NULL`

// Confirm turns a held booking into a confirmed one.
//
// Ownership is enforced inside the UPDATE predicate rather than by a separate
// SELECT-then-check, so there is no window between the check and the write.
//
// A repeat confirm is an idempotent success: the seats have already moved, so
// returning 409 would make a client retry look like a failure to the passenger.
func (r *Repository) Confirm(ctx context.Context, bookingID, customerID string) (*Booking, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var seats int
	err = tx.QueryRow(ctx, confirmBookingSQL, bookingID, customerID).Scan(&seats)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Lost the race, already confirmed, expired, or not ours. Load it to
		// find out which — but only rows this customer owns are visible here.
		existing, loadErr := r.loadBooking(ctx, tx, bookingID, customerID)
		if loadErr != nil {
			return nil, loadErr
		}
		if existing.Status != BookingConfirmed && existing.Status != BookingBoarded {
			return nil, ErrSeatsUnavailable
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return existing, nil
	case err != nil:
		return nil, err
	}

	if _, err := tx.Exec(ctx, moveHeldToBookedSQL, bookingIDTrip(ctx, tx, bookingID), seats); err != nil {
		return nil, err
	}

	out, err := r.loadBooking(ctx, tx, bookingID, customerID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) loadBooking(ctx context.Context, q pgx.Tx, bookingID, customerID string) (*Booking, error) {
	var b Booking
	err := q.QueryRow(ctx, loadBookingForCustomerSQL, bookingID, customerID).
		Scan(&b.ID, &b.TripID, &b.CustomerID, &b.Seats, &b.Status, &b.PricePerSeat, &b.HoldExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBookingNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func bookingIDTrip(ctx context.Context, q pgx.Tx, bookingID string) string {
	var tripID string
	_ = q.QueryRow(ctx, `SELECT trip_id FROM intercity_bookings WHERE id = $1`, bookingID).Scan(&tripID)
	return tripID
}

// UPDATE ... RETURNING yields the NEW row, so it cannot tell us which counter
// the seats are currently sitting in. The `prev` CTE snapshots the row (holding
// a lock on it) before the UPDATE runs, and RETURNING reads the OLD status back
// out of that snapshot.
//
// Getting this wrong is not subtle in effect: reading the post-update status
// makes every cancellation look like it came from HELD, so cancelling a
// CONFIRMED booking decrements held_seats — which is zero — and
// seats_non_negative aborts the transaction.
const cancelBookingSQL = `
	WITH prev AS (
	    SELECT id, trip_id, seats, status
	      FROM intercity_bookings
	     WHERE id = $1
	       AND customer_id = $2
	       AND status IN ('HELD', 'CONFIRMED')
	       AND deleted_at IS NULL
	     FOR UPDATE
	), upd AS (
	    UPDATE intercity_bookings b
	       SET status = 'CANCELLED', cancelled_at = NOW(),
	           cancelled_by_role = $3, updated_at = NOW()
	      FROM prev
	     WHERE b.id = prev.id
	    RETURNING prev.trip_id AS trip_id, prev.seats AS seats, prev.status AS prev_status
	)
	SELECT trip_id, seats, prev_status FROM upd`

// CancelBooking returns the seats to the pool from either HELD or CONFIRMED.
//
// The previous status is captured in the same statement that changes it, so the
// counter adjustment cannot be applied against a status that has since moved.
// Cancelling an already-cancelled booking is a no-op, not an error: the seats
// are already back.
func (r *Repository) CancelBooking(ctx context.Context, bookingID, customerID, role string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tripID, prevStatus string
	var seats int
	err = tx.QueryRow(ctx, cancelBookingSQL, bookingID, customerID, role).Scan(&tripID, &seats, &prevStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already terminal — nothing to release
	}
	if err != nil {
		return err
	}

	// prevStatus is the state the seats are currently counted in.
	column := "held_seats"
	if prevStatus == BookingConfirmed {
		column = "booked_seats"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE intercity_trips SET `+column+` = `+column+` - $2, updated_at = NOW() WHERE id = $1`,
		tripID, seats); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const claimExpiredHoldsSQL = `
	UPDATE intercity_bookings
	   SET status = 'EXPIRED', updated_at = NOW()
	 WHERE id IN (
	       SELECT id FROM intercity_bookings
	        WHERE status = 'HELD'
	          AND hold_expires_at < NOW()
	          AND deleted_at IS NULL
	        ORDER BY hold_expires_at
	        LIMIT $1
	        FOR UPDATE SKIP LOCKED)
	RETURNING trip_id, seats`

// SweepExpiredHolds releases seats whose hold lapsed.
//
// Each expired hold is released in its OWN transaction rather than one batch
// transaction. That matters: if a single trip's counters have drifted out of
// step with its bookings, decrementing would violate seats_non_negative and
// abort the whole batch — and because the sweeper re-selects the same oldest
// rows every cycle, it would abort on that same row forever, silently leaking
// every other expired hold in the system. Per-row isolation contains the damage
// to the one bad trip.
//
// A row that cannot be released is left HELD and reported, not swallowed and
// not force-corrected: the counters are money-adjacent inventory, so drift is
// something a human should see (reconciliation sweep (g) in the design), never
// something a background worker papers over with GREATEST(x, 0).
func (r *Repository) SweepExpiredHolds(ctx context.Context, limit int) (int, error) {
	rows, err := r.db.Query(ctx, selectExpiredHoldsSQL, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		bookingID string
		tripID    string
		seats     int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.bookingID, &c.tripID, &c.seats); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	released := 0
	var drifted []string
	for _, c := range candidates {
		if err := r.releaseOneHold(ctx, c.bookingID, c.tripID, c.seats); err != nil {
			drifted = append(drifted, c.tripID)
			continue
		}
		released++
	}
	if len(drifted) > 0 {
		return released, &DriftError{TripIDs: drifted}
	}
	return released, nil
}

const selectExpiredHoldsSQL = `
	SELECT id, trip_id, seats
	  FROM intercity_bookings
	 WHERE status = 'HELD'
	   AND hold_expires_at < NOW()
	   AND deleted_at IS NULL
	 ORDER BY hold_expires_at
	 LIMIT $1`

// releaseOneHold expires exactly one booking and returns its seats, atomically.
// The status change is conditional on the row still being HELD, so a concurrent
// confirm or a second sweeper replica cannot cause a double release.
func (r *Repository) releaseOneHold(ctx context.Context, bookingID, tripID string, seats int) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `
		UPDATE intercity_bookings SET status = 'EXPIRED', updated_at = NOW()
		 WHERE id = $1 AND status = 'HELD' AND deleted_at IS NULL`, bookingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // someone else transitioned it; nothing to release
	}

	if _, err := tx.Exec(ctx,
		`UPDATE intercity_trips SET held_seats = held_seats - $2, updated_at = NOW() WHERE id = $1`,
		tripID, seats); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
