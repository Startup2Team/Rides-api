//go:build integration

package dbit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/intercity"
)

// Confirm and the hold sweeper can act on the same expiring booking at the same
// instant. If either did a read-then-write, held_seats would be decremented
// twice — caught by seats_non_negative as a 500 on a real passenger, or worse,
// underflowing another passenger's hold. Every transition is therefore a
// conditional UPDATE on the BOOKING row first, and only the transaction that
// actually moved the row touches the trip counters.
func TestConfirm_RacesSweeperSafely(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	for i := 0; i < 15; i++ {
		tripID := insertIntercityTrip(t, ctx, 6)
		cust := insertIntercityCustomer(t, ctx)

		b, err := repo.Hold(ctx, tripID, cust, 2, uniqueKey("race"), 6)
		require.NoError(t, err)

		// Put the hold right on the edge so both racers see it as actionable.
		_, err = pool.Exec(ctx,
			`UPDATE intercity_bookings SET hold_expires_at = NOW() + interval '40 milliseconds' WHERE id = $1`, b.ID)
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); time.Sleep(35 * time.Millisecond); _, _ = repo.Confirm(ctx, b.ID, cust) }()
		go func() { defer wg.Done(); time.Sleep(35 * time.Millisecond); _, _ = repo.SweepExpiredHolds(ctx, 10) }()
		wg.Wait()

		var status string
		var held, booked int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT b.status, t.held_seats, t.booked_seats
			  FROM intercity_bookings b JOIN intercity_trips t ON t.id = b.trip_id
			 WHERE b.id = $1`, b.ID).Scan(&status, &held, &booked))

		// Which of the two wins is a genuine race and either outcome is correct;
		// HELD is also legitimate when both fire just before the expiry instant
		// and neither predicate matches. What must ALWAYS hold is that the trip
		// counters agree with whatever state the booking landed in — that is the
		// invariant a double-decrement would break.
		switch status {
		case intercity.BookingHeld:
			require.Equal(t, 2, held, "an untouched hold must still hold its seats")
			require.Equal(t, 0, booked)
		case intercity.BookingConfirmed:
			require.Equal(t, 0, held, "confirm must move seats out of held")
			require.Equal(t, 2, booked)
		case intercity.BookingExpired:
			require.Equal(t, 0, held, "the sweeper must release the seats")
			require.Equal(t, 0, booked)
		default:
			t.Fatalf("booking ended in an impossible state: %s", status)
		}
	}
}

// Two concurrent confirms, or a client retry, must move the seats exactly once.
func TestConfirm_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)

	b, err := repo.Hold(ctx, tripID, cust, 2, "confirm-idem", 6)
	require.NoError(t, err)

	first, err := repo.Confirm(ctx, b.ID, cust)
	require.NoError(t, err)
	require.Equal(t, intercity.BookingConfirmed, first.Status)

	second, err := repo.Confirm(ctx, b.ID, cust)
	require.NoError(t, err, "a repeat confirm must be an idempotent success, not 409")
	require.Equal(t, intercity.BookingConfirmed, second.Status)

	var held, booked int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats, booked_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held, &booked))
	require.Equal(t, 0, held)
	require.Equal(t, 2, booked, "seats must move exactly once")
}

// Ownership is enforced in the SQL predicate, not by a separate check: another
// customer must not be able to confirm someone else's booking.
func TestConfirm_RejectsAnotherCustomersBooking(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 6)
	owner := insertIntercityCustomer(t, ctx)
	attacker := insertIntercityCustomer(t, ctx)

	b, err := repo.Hold(ctx, tripID, owner, 1, "owner-key", 6)
	require.NoError(t, err)

	_, err = repo.Confirm(ctx, b.ID, attacker)
	require.Error(t, err, "a different customer must not confirm this booking")

	var status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM intercity_bookings WHERE id = $1`, b.ID).Scan(&status))
	require.Equal(t, intercity.BookingHeld, status, "the booking must be untouched")
}

// The sweeper must decrement only for rows it actually transitioned, and must
// leave live holds alone.
func TestSweeper_OnlyReleasesRowsItTransitioned(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	// The sweeper is GLOBAL and processes oldest-first, and this database
	// persists between runs. Without draining, an accumulated backlog of expired
	// holds fills the batch and this test's own hold is never reached. Drain to
	// a fixed point first so the assertions below describe only our trip.
	// DriftError here is expected and is NOT a product failure: the schema tests
	// insert bookings with raw SQL to exercise DDL constraints, which correctly
	// bypasses the counters. The sweeper reports those trips and skips them —
	// which is exactly the per-row resilience being relied on. Anything else is
	// a real error.
	for {
		n, err := repo.SweepExpiredHolds(ctx, 500)
		if err != nil {
			var drift *intercity.DriftError
			require.ErrorAs(t, err, &drift, "only counter drift may be tolerated here")
		}
		if n == 0 {
			break
		}
	}

	tripID := insertIntercityTrip(t, ctx, 6)

	expired, err := repo.Hold(ctx, tripID, insertIntercityCustomer(t, ctx), 2, "sweep-old", 6)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE intercity_bookings SET hold_expires_at = NOW() - interval '1 minute' WHERE id = $1`, expired.ID)
	require.NoError(t, err)

	live, err := repo.Hold(ctx, tripID, insertIntercityCustomer(t, ctx), 1, "sweep-live", 6)
	require.NoError(t, err)

	n, err := repo.SweepExpiredHolds(ctx, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)

	var held int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held))
	require.Equal(t, 1, held, "only the expired hold's seats may be released")

	var liveStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM intercity_bookings WHERE id = $1`, live.ID).Scan(&liveStatus))
	require.Equal(t, intercity.BookingHeld, liveStatus, "a live hold must survive the sweep")

	// Sweeping again must not double-release.
	_, err = repo.SweepExpiredHolds(ctx, 100)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held))
	require.Equal(t, 1, held, "a second sweep must be a no-op")
}

// Cancelling returns the seats to the pool, from either HELD or CONFIRMED, and
// records who did it — Decision 4 needs that attribution.
func TestCancelBooking_ReleasesSeatsFromEitherState(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	for _, tc := range []struct {
		name    string
		confirm bool
	}{
		{"from HELD", false},
		{"from CONFIRMED", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tripID := insertIntercityTrip(t, ctx, 6)
			cust := insertIntercityCustomer(t, ctx)

			b, err := repo.Hold(ctx, tripID, cust, 2, uniqueKey("cancel"), 6)
			require.NoError(t, err)
			if tc.confirm {
				_, err = repo.Confirm(ctx, b.ID, cust)
				require.NoError(t, err)
			}

			require.NoError(t, repo.CancelBooking(ctx, b.ID, cust, "CUSTOMER"))

			var held, booked int
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT held_seats, booked_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held, &booked))
			require.Zero(t, held)
			require.Zero(t, booked, "cancelled seats must return to the pool")

			var role string
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT cancelled_by_role FROM intercity_bookings WHERE id = $1`, b.ID).Scan(&role))
			require.Equal(t, "CUSTOMER", role)

			// Cancelling twice must not release the seats twice.
			require.NoError(t, repo.CancelBooking(ctx, b.ID, cust, "CUSTOMER"))
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT held_seats, booked_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held, &booked))
			require.Zero(t, held)
			require.Zero(t, booked)
		})
	}
}
