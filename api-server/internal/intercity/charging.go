package intercity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/workspace/ride-platform/internal/packages"
)

// NoShowBudgetPercent caps how much of a trip a driver can discount by marking
// passengers absent.
//
// Departure-charging gives the driver a direct financial motive to mark real
// passengers as no-shows, and marking no-shows is ALSO an honest driver's only
// defence against booking-bombing — so the two are indistinguishable by rate
// alone and cannot be policed after the fact. Capping the discount breaks the
// economics without punishing the honest case: marks beyond this share are
// still recorded (and still notify the passenger) but no longer reduce the bill.
const NoShowBudgetPercent = 40

// chargeableSeatsSQL computes what a trip owes, in SEATS.
//
// One expression over the cumulative ever-confirmed cohort, never a max() of two
// partial views: CONFIRMED and BOARDED are DISJOINT statuses, so a
// partially-boarded manifest — the normal case, since marking is optional —
// would undercharge under any formula that reads only one of them.
//
// v1 charged per booking (one credit for a six-seat booking, an 83% loss) and
// v2 reintroduced the same bug by counting booking ROWS. Everything here sums
// `seats`.
const chargeableSeatsSQL = `
	WITH b AS (
	    SELECT
	      COALESCE(SUM(seats) FILTER (
	        WHERE status IN ('CONFIRMED','BOARDED','COMPLETED')), 0) AS sold,
	      COALESCE(SUM(seats) FILTER (WHERE status = 'NO_SHOW'), 0)  AS no_shows
	      FROM intercity_bookings
	     WHERE trip_id = $1 AND deleted_at IS NULL
	), t AS (
	    SELECT total_seats FROM intercity_trips WHERE id = $1
	)
	SELECT GREATEST(
	         0,
	         (b.sold + b.no_shows)
	           - LEAST(b.no_shows, (t.total_seats * $2::int) / 100)
	       )
	  FROM b, t`

// CreateObligations records what a trip's driver owes, one row per chargeable
// seat, and is safe to run repeatedly.
//
// The debt is durable the moment this commits. Settlement happens afterwards and
// asynchronously, because per-seat charging is N separate ledger transactions
// and NO point in a request can make them atomic with the seat move — so the
// design records an obligation transactionally and settles it idempotently
// instead of pretending atomicity it cannot have.
func (r *Repository) CreateObligations(ctx context.Context, tripID string) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var driverID, vehicleTypeID string
	err = tx.QueryRow(ctx, `
		SELECT t.driver_id, v.vehicle_type_id
		  FROM intercity_trips t
		  JOIN driver_vehicles v ON v.id = t.vehicle_id
		 WHERE t.id = $1 AND t.deleted_at IS NULL`, tripID).Scan(&driverID, &vehicleTypeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTripNotFound
	}
	if err != nil {
		return 0, err
	}

	var chargeable int
	if err := tx.QueryRow(ctx, chargeableSeatsSQL, tripID, NoShowBudgetPercent).Scan(&chargeable); err != nil {
		return 0, err
	}
	if chargeable == 0 {
		return 0, tx.Commit(ctx)
	}

	// One row per credit. ON CONFLICT makes re-running a no-op, and because the
	// key is derived from (trip, seq) the two unique indexes can only ever agree.
	tag, err := tx.Exec(ctx, `
		INSERT INTO intercity_credit_charges
		    (trip_id, driver_id, vehicle_type_id, seq, idempotency_key)
		-- $1 is passed as TEXT and cast explicitly at each use: used bare as both
		-- a uuid column value and a string operand, Postgres cannot deduce one
		-- type for it and raises 42P08 at prepare time.
		SELECT $1::uuid, $2::uuid, $3::uuid, s,
		       'intercity:' || $1 || ':' || s::text
		  FROM generate_series(1, $4::int) AS s
		ON CONFLICT (trip_id, seq) DO NOTHING`,
		tripID, driverID, vehicleTypeID, chargeable)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// RunDepartures creates obligations for every trip whose departure time has
// arrived, on the SERVER's clock.
//
// This is why it exists at all: if obligations were only created by the driver
// calling /start, then simply never pressing Start would be a completely free
// trip — publish, fill the vehicle, drive it, collect the cash, and the backstop
// sweeper closes the trip having charged nothing, with no forensic trace and
// nothing to distinguish it from a crashed app.
func (r *Repository) RunDepartures(ctx context.Context, limit int) (int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT t.id FROM intercity_trips t
		 WHERE t.deleted_at IS NULL
		   AND t.status IN ('OPEN','BOARDING','IN_TRANSIT')
		   AND t.depart_at <= NOW()
		   AND NOT EXISTS (SELECT 1 FROM intercity_credit_charges c WHERE c.trip_id = t.id)
		 ORDER BY t.depart_at
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

	done := 0
	for _, id := range ids {
		if _, err := r.CreateObligations(ctx, id); err != nil {
			continue // one bad trip must not stop the rest
		}
		done++
	}
	return done, nil
}

// SettleCharges drains pending obligations into the entitlement ledger.
//
// The claim is COMMITTED before the money call. Holding it across
// DeductForIntercity would occupy two pooled connections per row — the claim's
// transaction plus the one deductOneUnit opens on a different connection — and
// under a backlog that self-deadlocks the pool and takes down the whole API,
// far beyond intercity. Exactly-once does not need the long lock: the ledger's
// unique idempotency key already provides it.
func (r *Repository) SettleCharges(ctx context.Context, ledger *packages.LedgerService, limit int) (int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, driver_id, vehicle_type_id, idempotency_key, trip_id
		  FROM intercity_credit_charges
		 WHERE status = 'PENDING' AND next_attempt_at <= NOW()
		   AND (leased_until IS NULL OR leased_until < NOW())
		 ORDER BY next_attempt_at
		 LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type charge struct{ id, driverID, vehicleTypeID, key, tripID string }
	var pending []charge
	for rows.Next() {
		var c charge
		if err := rows.Scan(&c.id, &c.driverID, &c.vehicleTypeID, &c.key, &c.tripID); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	settled := 0
	for _, c := range pending {
		// Lease in its own short transaction; zero rows means another replica
		// took it.
		tag, err := r.db.Exec(ctx, `
			UPDATE intercity_credit_charges
			   SET leased_until = NOW() + interval '60 seconds', attempts = attempts + 1, updated_at = NOW()
			 WHERE id = $1 AND status = 'PENDING'
			   AND (leased_until IS NULL OR leased_until < NOW())`, c.id)
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}

		charged, err := ledger.DeductForIntercity(ctx, c.driverID, c.vehicleTypeID, c.key, c.tripID)
		switch {
		case err == nil:
			// charged==false means the ledger already holds this key: ALREADY
			// CHARGED, not failed. Treating it as a failure would bill a driver
			// and then penalise them for non-payment.
			if _, uerr := r.db.Exec(ctx, `
				UPDATE intercity_credit_charges
				   SET status='CHARGED', charged_at=NOW(), leased_until=NULL, updated_at=NOW()
				 WHERE id=$1`, c.id); uerr == nil {
				settled++
			}
			_ = charged
		case errors.Is(err, packages.ErrNoCredits):
			// Only credit failures may ever promote to arrears. Five transient
			// errors must not write off a collectable debt.
			_, _ = r.db.Exec(ctx, `
				UPDATE intercity_credit_charges
				   SET no_credit_attempts = no_credit_attempts + 1,
				       next_attempt_at = NOW() + (interval '1 minute' * LEAST(no_credit_attempts + 1, 60)),
				       leased_until = NULL, last_error = 'no credits', updated_at = NOW()
				 WHERE id=$1`, c.id)
		default:
			_, _ = r.db.Exec(ctx, `
				UPDATE intercity_credit_charges
				   SET next_attempt_at = NOW() + (interval '1 minute' * LEAST(attempts, 60)),
				       leased_until = NULL, last_error = $2, updated_at = NOW()
				 WHERE id=$1`, c.id, err.Error())
		}
	}
	return settled, nil
}
