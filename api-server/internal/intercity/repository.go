package intercity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository owns every SQL statement in the intercity product. Seat
// acquisition and state transitions live here rather than in the service
// because their correctness is a property of the statements themselves.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// acquireSeatsSQL is the heart of the product.
//
// It is ONE conditional UPDATE, never a SELECT followed by an UPDATE. Two
// concurrent executions targeting the same row serialise on the row lock, and
// when the second is released Postgres re-evaluates this WHERE clause against
// the updated row version (EvalPlanQual). Because every predicate references
// only that row's own columns, the re-check is exact: no lost update, no
// phantom, no deadlock (single row, single statement).
//
// This is correct ONLY at READ COMMITTED. At REPEATABLE READ or SERIALIZABLE
// the statement raises a serialization failure instead of re-checking, so the
// surrounding transaction must never raise its isolation level.
//
// The seats_never_oversold CHECK constraint backs this up: even if this
// statement were later changed incorrectly, an oversell cannot be persisted.
const acquireSeatsSQL = `
	UPDATE intercity_trips
	   SET held_seats = held_seats + $2, updated_at = NOW()
	 WHERE id = $1
	   AND deleted_at IS NULL
	   AND status = 'OPEN'
	   AND depart_at > NOW() + interval '5 minutes'
	   AND booked_seats + held_seats + $2 <= total_seats
	   AND booked_seats + held_seats + $2 <= $3
	RETURNING total_seats - booked_seats - held_seats`

// insertHoldSQL inserts the booking BEFORE any counter moves, so that a client
// retry of an already-committed hold is caught by the idempotency index and
// never reaches the counter update. Only a row that was genuinely inserted goes
// on to consume seats.
//
// The price is copied from the trip inside the same statement: a booked
// passenger pays the price that was advertised when they booked, even if the
// driver later edits the trip.
const insertHoldSQL = `
	INSERT INTO intercity_bookings
	    (trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at, idempotency_key)
	SELECT $1, $2, $3, 'HELD', t.price_per_seat_rwf, NOW() + interval '5 minutes', $4
	  FROM intercity_trips t
	 WHERE t.id = $1 AND t.deleted_at IS NULL
	ON CONFLICT (customer_id, idempotency_key) WHERE idempotency_key IS NOT NULL
	DO NOTHING
	RETURNING id, seats, price_per_seat_rwf`

const findHoldByIdemSQL = `
	SELECT id, trip_id, customer_id, seats, status, price_per_seat_rwf, hold_expires_at
	  FROM intercity_bookings
	 WHERE customer_id = $1 AND idempotency_key = $2`

// Hold reserves seats for HoldTTL.
//
// Lock order throughout this package is ALWAYS booking row, then trip row.
// Every other transition must use the same order; reversing it anywhere
// produces an ABBA deadlock against this path.
//
// sellable caps availability at what the driver's credit balance can cover. It
// is enforced here and never returned to a caller: exposing the clamped number
// would be a live oracle of a rival driver's balance.
func (r *Repository) Hold(ctx context.Context, tripID, customerID string, seats int, idemKey string, sellable int) (*Booking, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	b := Booking{TripID: tripID, CustomerID: customerID, Status: BookingHeld}
	err = tx.QueryRow(ctx, insertHoldSQL, tripID, customerID, seats, idemKey).
		Scan(&b.ID, &b.Seats, &b.PricePerSeat)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Either a retry of a committed hold, or the trip does not exist. The
		// former must return the original booking and touch no counters.
		existing, findErr := r.findBookingByIdemKey(ctx, tx, customerID, idemKey)
		if findErr != nil {
			return nil, findErr
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return existing, nil
	case err != nil:
		return nil, err
	}

	var remaining int
	err = tx.QueryRow(ctx, acquireSeatsSQL, tripID, seats, sellable).Scan(&remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSeatsUnavailable
	}
	if err != nil {
		return nil, err
	}
	b.Remaining = remaining

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *Repository) findBookingByIdemKey(ctx context.Context, q pgx.Tx, customerID, idemKey string) (*Booking, error) {
	var b Booking
	err := q.QueryRow(ctx, findHoldByIdemSQL, customerID, idemKey).
		Scan(&b.ID, &b.TripID, &b.CustomerID, &b.Seats, &b.Status, &b.PricePerSeat, &b.HoldExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// No prior booking with this key, so the INSERT matched no trip.
		return nil, ErrSeatsUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}
