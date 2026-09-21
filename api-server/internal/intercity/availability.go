package intercity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LockoutLead is how long before departure a driver stops receiving city ride
// offers. Before this they keep earning; after it, a city ride could run past
// their departure and strand a vehicle full of booked passengers.
const LockoutLead = "30 minutes"

// StaleTripAge is how long past departure a non-terminal trip may linger before
// the backstop sweeper force-completes it. This is LIVENESS, not correctness:
// the availability gate is a timestamp that expires on its own, so a missed
// transition cannot strand a driver — the sweeper only tidies up.
const StaleTripAge = "12 hours"

// recomputeCommitmentSQL derives a driver's commitment from their LIVE trips
// instead of setting or clearing a flag.
//
// This matters more than it looks. uq_intercity_trip_driver_slot is
// exact-timestamp equality, so a driver CAN hold two overlapping trips — 08:00
// and 08:30 on a three-hour corridor both insert cleanly. Clearing
// unconditionally when the first one completes would hand that driver city ride
// offers while their second vehicle is still boarding.
//
// Deriving instead of clearing also makes the operation idempotent and
// self-healing: running it twice, or against a driver who was never committed,
// converges on the same correct answer.
//
// MAX over live trips, and NULL when there are none, so the safe default is
// VISIBLE. Every failure mode of this column makes a driver MORE available,
// never invisible.
const recomputeCommitmentSQL = `
	UPDATE driver_profiles dp
	   SET intercity_committed_until = (
	         SELECT MAX(it.depart_at + INTERVAL '` + LockoutLead + `')
	           FROM intercity_trips it
	          WHERE it.driver_id = dp.id
	            AND it.deleted_at IS NULL
	            AND it.status IN ('OPEN', 'BOARDING', 'IN_TRANSIT')
	            AND it.depart_at <= NOW() + INTERVAL '` + LockoutLead + `')
	 WHERE dp.id = $1`

// RecomputeDriverCommitment refreshes one driver's availability gate from their
// live trips. Safe to call at any time, from any path.
func (r *Repository) RecomputeDriverCommitment(ctx context.Context, driverProfileID string) error {
	_, err := r.db.Exec(ctx, recomputeCommitmentSQL, driverProfileID)
	return err
}

// LockDriverForDeparture commits the trip's ASSIGNED driver — never the
// operator. An owner who does not drive was never in city dispatch to begin
// with, so there is nothing to remove them from.
func (r *Repository) LockDriverForDeparture(ctx context.Context, tripID string) error {
	var driverID string
	err := r.db.QueryRow(ctx,
		`SELECT driver_id FROM intercity_trips WHERE id = $1 AND deleted_at IS NULL`, tripID).
		Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return err
	}
	return r.RecomputeDriverCommitment(ctx, driverID)
}

// SetTripStatus moves a trip and refreshes its driver's availability in the
// SAME transaction, so the two can never disagree. Every status change goes
// through here precisely so no caller can forget the second half.
func (r *Repository) SetTripStatus(ctx context.Context, tripID, status string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var driverID string
	err = tx.QueryRow(ctx, `
		UPDATE intercity_trips
		   SET status = $2::text,
		       -- $2 is cast explicitly everywhere: used bare as both the assigned
		       -- value and a comparison operand, Postgres cannot deduce one type
		       -- for it and raises 42P08 at prepare time.
		       started_at   = CASE WHEN $2::text = 'IN_TRANSIT' THEN NOW() ELSE started_at END,
		       completed_at = CASE WHEN $2::text = 'COMPLETED'  THEN NOW() ELSE completed_at END,
		       cancelled_at = CASE WHEN $2::text = 'CANCELLED'  THEN NOW() ELSE cancelled_at END,
		       updated_at = NOW()
		 WHERE id = $1 AND deleted_at IS NULL
		RETURNING driver_id`, tripID, status).Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, recomputeCommitmentSQL, driverID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SweepStaleTrips force-completes non-terminal trips well past departure and
// refreshes their drivers' availability.
//
// Each trip is handled in its own transaction for the same reason the hold
// sweeper is: one bad row must not abort the batch and then abort it again on
// every subsequent cycle.
func (r *Repository) SweepStaleTrips(ctx context.Context, limit int) (int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id FROM intercity_trips
		 WHERE deleted_at IS NULL
		   AND status IN ('OPEN', 'BOARDING', 'IN_TRANSIT')
		   AND depart_at < NOW() - INTERVAL '`+StaleTripAge+`'
		 ORDER BY depart_at
		 LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	swept := 0
	for _, id := range ids {
		if err := r.SetTripStatus(ctx, id, TripCompleted); err != nil {
			continue // reported by the caller's metrics; one bad trip must not stop the rest
		}
		swept++
	}
	return swept, nil
}
