//go:build integration

package dbit

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/intercity"
)

// The oversell race, which is the entire reason the seat design exists: many
// passengers reading "N seats left" at the same instant and all booking. A
// SELECT-then-UPDATE cannot survive this; a single conditional UPDATE can,
// because Postgres re-evaluates the WHERE against the updated row after the
// row lock is released (EvalPlanQual).
//
// Customers are created up front: require.NoError calls t.FailNow, which must
// not be invoked from a non-test goroutine.
func TestHold_ConcurrentHoldsNeverOversell(t *testing.T) {
	ctx := context.Background()
	const totalSeats = 6
	const workers = 20
	const seatsEach = 2

	tripID := insertIntercityTrip(t, ctx, totalSeats)
	repo := intercity.NewRepository(pool)

	customers := make([]string, workers)
	for i := range customers {
		customers[i] = insertIntercityCustomer(t, ctx)
	}

	type outcome struct {
		seats int
		err   error
	}
	results := make(chan outcome, workers)
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start // release everyone at once to maximise contention
			b, err := repo.Hold(ctx, tripID, customers[n], seatsEach,
				fmt.Sprintf("race-key-%d", n), totalSeats)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			results <- outcome{seats: b.Seats}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	granted, failed := 0, 0
	for r := range results {
		if r.err != nil {
			require.ErrorIs(t, r.err, intercity.ErrSeatsUnavailable,
				"a losing hold must fail cleanly, not with an unexpected error")
			failed++
			continue
		}
		granted += r.seats
	}

	require.LessOrEqual(t, granted, totalSeats,
		"OVERSOLD: granted %d seats on a %d-seat vehicle", granted, totalSeats)
	require.Equal(t, workers, granted/seatsEach+failed, "every worker must have an outcome")

	var held, booked int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats, booked_seats FROM intercity_trips WHERE id = $1`, tripID).
		Scan(&held, &booked))
	require.Equal(t, granted, held+booked,
		"counters must equal granted seats exactly — drift means a lost or double update")

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(seats), 0) FROM intercity_bookings
		 WHERE trip_id = $1 AND deleted_at IS NULL AND status = 'HELD'`, tripID).Scan(&rows))
	require.Equal(t, granted, rows, "booking rows must agree with the trip counters")
}

// A client whose request committed but whose response was lost will retry. The
// retry must return the SAME booking and must not increment held_seats again.
func TestHold_RetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	cust := insertIntercityCustomer(t, ctx)
	repo := intercity.NewRepository(pool)

	first, err := repo.Hold(ctx, tripID, cust, 2, "same-key", 6)
	require.NoError(t, err)

	second, err := repo.Hold(ctx, tripID, cust, 2, "same-key", 6)
	require.NoError(t, err, "a retry must succeed, not 409")
	require.Equal(t, first.ID, second.ID, "the retry must return the SAME booking")

	var held int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT held_seats FROM intercity_trips WHERE id = $1`, tripID).Scan(&held))
	require.Equal(t, 2, held, "a retry must NOT double-increment held_seats")
}

// sellable_seats clamps availability to what the driver can actually pay for.
// It is enforced inside the UPDATE and never exposed to customers — publishing
// it would be a live oracle of a rival driver's credit balance.
func TestHold_RespectsSellableClamp(t *testing.T) {
	ctx := context.Background()
	tripID := insertIntercityTrip(t, ctx, 6)
	repo := intercity.NewRepository(pool)

	_, err := repo.Hold(ctx, tripID, insertIntercityCustomer(t, ctx), 2, "clamp-1", 2)
	require.NoError(t, err)

	_, err = repo.Hold(ctx, tripID, insertIntercityCustomer(t, ctx), 1, "clamp-2", 2)
	require.ErrorIs(t, err, intercity.ErrSeatsUnavailable,
		"the clamp must bite even though the vehicle has 4 physical seats free")
}

// A hold must not be possible on a trip about to leave, or on one that is not
// OPEN. Both collapse to the same generic error: zero rows conflates several
// causes and the message must not leak which.
func TestHold_RejectsClosedAndDepartingTrips(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	departing := insertIntercityTrip(t, ctx, 6)
	_, err := pool.Exec(ctx,
		`UPDATE intercity_trips SET depart_at = NOW() + interval '2 minutes' WHERE id = $1`, departing)
	require.NoError(t, err)
	_, err = repo.Hold(ctx, departing, insertIntercityCustomer(t, ctx), 1, "cutoff", 6)
	require.ErrorIs(t, err, intercity.ErrSeatsUnavailable, "inside the 5-minute cutoff")

	boarding := insertIntercityTrip(t, ctx, 6)
	_, err = pool.Exec(ctx,
		`UPDATE intercity_trips SET status = 'BOARDING' WHERE id = $1`, boarding)
	require.NoError(t, err)
	_, err = repo.Hold(ctx, boarding, insertIntercityCustomer(t, ctx), 1, "notopen", 6)
	require.ErrorIs(t, err, intercity.ErrSeatsUnavailable, "trip no longer OPEN")
}
