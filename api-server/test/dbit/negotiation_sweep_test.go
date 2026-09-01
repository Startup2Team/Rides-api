//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/notification"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/tracking"
)

// Regression coverage for the durable NEGOTIATING backstop: before this fix
// StartNegotiationTimeout's 5-minute clock was a bare time.AfterFunc, wiped by
// any deploy/restart, with nothing but a 15-minute Redis TTL cleaning up the
// cache — the Postgres row itself could sit NEGOTIATING forever.
// CancelExpiredNegotiations (cmd/server/main.go's 30s sweep) is the belt-and-
// suspenders backstop: it re-checks status AND deadline atomically at write
// time, not just at scan time, so it never cancels a ride that has since
// progressed or had its deadline pushed out by a counter-offer.

func newTestRideServiceForSweep(t *testing.T) (*ride.Service, *ride.Repository) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	hub := tracking.NewHub(rdb, zerolog.Nop())
	t.Cleanup(func() { hub.Close() })

	notifySvc := notification.New(&config.Config{}, zerolog.Nop())
	notifySvc.SetRepository(notification.NewRepository(pool))
	ana := analytics.NewService(pool, rdb, zerolog.Nop())

	repo := ride.NewRepository(pool)
	svc := ride.NewService(repo, rdb, notifySvc, ana, hub, &config.Config{}, zerolog.Nop())
	return svc, repo
}

func setupNegotiatingRide(t *testing.T, ctx context.Context, repo *ride.Repository) (rideID, customerID, driverUserID string) {
	t.Helper()
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-driver", "android", nil, nil)
	require.NoError(t, err)
	profileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	rideID = createTestRide(t, ctx, repo, customer.ID, profileID, ride.StatusNegotiating)
	return rideID, customer.ID, driverUser.ID
}

func TestCancelExpiredNegotiations_PastDeadline_CancelledAndNotified(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceForSweep(t)
	rideID, customerID, _ := setupNegotiatingRide(t, ctx, repo)

	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(-time.Minute)))

	n, err := svc.CancelExpiredNegotiations(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusCancelled, r.Status)

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'ride' AND title = $2`,
		customerID, "Ride cancelled").Scan(&count))
	require.Equal(t, 1, count, "customer must get an FCM-backed cancellation notice")
}

func TestCancelExpiredNegotiations_FutureDeadline_NotTouched(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceForSweep(t)
	rideID, _, _ := setupNegotiatingRide(t, ctx, repo)

	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(5*time.Minute)))

	n, err := svc.CancelExpiredNegotiations(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusNegotiating, r.Status)
}

func TestCancelExpiredNegotiations_NullDeadline_NotTouched(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceForSweep(t)
	rideID, _, _ := setupNegotiatingRide(t, ctx, repo)
	// No SetNegotiationDeadline call — mirrors a ride created before this
	// migration, or one whose deadline write silently failed.

	n, err := svc.CancelExpiredNegotiations(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusNegotiating, r.Status)
}

// End-to-end: a ride that progressed to CONFIRMED (fare agreed) before the
// sweep ever runs must never be cancelled, even though its stale deadline
// from the NEGOTIATING phase already expired. FindExpiredNegotiations' own
// WHERE clause (status = 'NEGOTIATING') already excludes it here — this locks
// in that end-to-end behavior, but NOT the harder race below.
func TestCancelExpiredNegotiations_ProgressedPastNegotiating_NotCancelled(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceForSweep(t)
	rideID, _, _ := setupNegotiatingRide(t, ctx, repo)

	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(-time.Minute)))
	// Fare agreed and the ride moved on — same transition CONFIRM would apply.
	require.NoError(t, repo.Transition(ctx, rideID, ride.StatusNegotiating, ride.StatusConfirmed))

	n, err := svc.CancelExpiredNegotiations(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n, "a ride that progressed past NEGOTIATING must never be cancelled by the sweep, even with a stale expired deadline")

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusConfirmed, r.Status)
}

// This is the real TOCTOU-safety test, at the repo layer: FindExpiredNegotiations'
// scan can only see the row's state AT SCAN TIME, so the property that
// actually matters is what CancelIfStillNegotiating's own WHERE clause does
// when the row changed underneath it AFTER being selected as a candidate and
// BEFORE the cancel write — e.g. the customer accepted the fare in the
// instant between the sweep's SELECT and its per-row UPDATE. Simulated here
// by mutating the row directly, then calling CancelIfStillNegotiating exactly
// as the sweep loop does. A version of this fix that used the generic
// repo.Cancel (whose guard is only "not already terminal") would wrongly
// cancel this ride — CancelIfStillNegotiating must not.
func TestCancelIfStillNegotiating_RowChangedAfterSelection_NotCancelled(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRideServiceForSweep(t)
	rideID, _, _ := setupNegotiatingRide(t, ctx, repo)

	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(-time.Minute)))
	// The row would have matched FindExpiredNegotiations' SELECT right up
	// until this instant — then the fare gets agreed and it moves on.
	require.NoError(t, repo.Transition(ctx, rideID, ride.StatusNegotiating, ride.StatusConfirmed))

	didCancel, err := repo.CancelIfStillNegotiating(ctx, rideID)
	require.NoError(t, err)
	require.False(t, didCancel, "must not cancel a ride that is no longer NEGOTIATING at write time, even with an already-expired deadline")

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusConfirmed, r.Status)
}

// A counter-offer's ResetNegotiationTimeout push-out must be honoured: a ride
// whose deadline was reset to the future (exactly what
// persistNegotiationDeadline does on every counter-offer) must not be swept
// even though it once had an expired deadline.
func TestCancelExpiredNegotiations_DeadlineExtendedByCounterOffer_NotCancelled(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceForSweep(t)
	rideID, _, _ := setupNegotiatingRide(t, ctx, repo)

	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(-time.Minute)))
	// A counter-offer arrives and pushes the deadline back out, exactly what
	// ResetNegotiationTimeout does via persistNegotiationDeadline.
	require.NoError(t, repo.SetNegotiationDeadline(ctx, rideID, time.Now().Add(5*time.Minute)))

	n, err := svc.CancelExpiredNegotiations(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusNegotiating, r.Status)
}
