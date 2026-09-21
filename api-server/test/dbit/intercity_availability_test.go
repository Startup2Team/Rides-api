//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/intercity"
	"github.com/workspace/ride-platform/pkg/geo"
)

// A committed driver must disappear from CITY dispatch shortly before their
// intercity departure, and must reappear afterwards.
//
// The danger here is asymmetric. Leaking a committed driver into city dispatch
// costs one declined offer. Stranding them costs a driver who is online,
// pinging, indexed — and silently skipped forever, earning nothing, with no
// error anywhere. Every test below exists for the second failure, not the first.
func TestAvailability_CommitmentHidesDriverFromCityDispatch(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 6)
	driverID := tripDriverID(t, ctx, tripID)

	require.NoError(t, repo.LockDriverForDeparture(ctx, tripID))
	require.Nil(t, committedUntil(t, ctx, driverID),
		"a trip two hours out must NOT commit the driver — they keep earning on city rides "+
			"right up to the lockout window")

	enterLockoutWindow(t, ctx, tripID)
	require.NoError(t, repo.LockDriverForDeparture(ctx, tripID))
	require.NotNil(t, committedUntil(t, ctx, driverID), "lockout must commit the driver")
}

// Every path that ends a commitment must give the driver back to city dispatch.
// A miss here is the zombie-state failure the design names as its top risk.
func TestAvailability_RestoredOnEveryExitPath(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	for _, tc := range []struct {
		name string
		end  func(t *testing.T, tripID string)
	}{
		{"trip completes", func(t *testing.T, tripID string) {
			require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCompleted))
		}},
		{"driver cancels the trip", func(t *testing.T, tripID string) {
			require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCancelled))
		}},
		{"trip departs unfilled and completes", func(t *testing.T, tripID string) {
			require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripInTransit))
			require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCompleted))
		}},
		{"stale trip swept after being abandoned", func(t *testing.T, tripID string) {
			_, err := pool.Exec(ctx,
				`UPDATE intercity_trips SET depart_at = NOW() - interval '13 hours' WHERE id = $1`, tripID)
			require.NoError(t, err)
			_, err = repo.SweepStaleTrips(ctx, 50)
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tripID := insertIntercityTrip(t, ctx, 6)
			driverID := tripDriverID(t, ctx, tripID)

			enterLockoutWindow(t, ctx, tripID)
			require.NoError(t, repo.LockDriverForDeparture(ctx, tripID))
			require.NotNil(t, committedUntil(t, ctx, driverID), "precondition: committed")

			tc.end(t, tripID)

			require.Nil(t, committedUntil(t, ctx, driverID),
				"driver must be released back to city dispatch — a missed clear strands them invisibly")
		})
	}
}

// The DBA review found that uq_intercity_trip_driver_slot is exact-timestamp
// equality, so a driver CAN hold two overlapping trips (08:00 and 08:30).
// Clearing unconditionally on the first one to finish would put a driver back
// into city dispatch while their second trip is still boarding.
func TestAvailability_OverlappingTripsClearConditionally(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	first := insertIntercityTrip(t, ctx, 6)
	driverID := tripDriverID(t, ctx, first)
	second := insertTripForDriver(t, ctx, driverID, 6, "20 minutes")

	enterLockoutWindow(t, ctx, first)
	require.NoError(t, repo.LockDriverForDeparture(ctx, first))
	require.NoError(t, repo.LockDriverForDeparture(ctx, second))

	// The earlier trip finishes; the later one is still live.
	require.NoError(t, repo.SetTripStatus(ctx, first, intercity.TripCompleted))

	got := committedUntil(t, ctx, driverID)
	require.NotNil(t, got,
		"the driver is STILL committed to the second trip — clearing unconditionally would "+
			"hand them city rides while a booked vehicle waits for them")

	require.NoError(t, repo.SetTripStatus(ctx, second, intercity.TripCompleted))
	require.Nil(t, committedUntil(t, ctx, driverID), "released once nothing is live")
}

// Restoring is idempotent and self-healing: running the release twice, or
// against a driver who was never committed, must not corrupt anything.
func TestAvailability_ReleaseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 6)
	driverID := tripDriverID(t, ctx, tripID)

	enterLockoutWindow(t, ctx, tripID)
	require.NoError(t, repo.LockDriverForDeparture(ctx, tripID))
	require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCompleted))
	require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCompleted))
	require.Nil(t, committedUntil(t, ctx, driverID))
}

// --- helpers --------------------------------------------------------------

// enterLockoutWindow moves a trip to just inside its lockout window, which is
// what the departure worker observes when it fires at depart_at - 30 minutes.
func enterLockoutWindow(t *testing.T, ctx context.Context, tripID string) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`UPDATE intercity_trips SET depart_at = NOW() + interval '10 minutes' WHERE id = $1`, tripID)
	require.NoError(t, err)
}

func tripDriverID(t *testing.T, ctx context.Context, tripID string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT driver_id FROM intercity_trips WHERE id = $1`, tripID).Scan(&id))
	return id
}

func committedUntil(t *testing.T, ctx context.Context, driverID string) *string {
	t.Helper()
	var v *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT CASE WHEN intercity_committed_until IS NULL
		            OR intercity_committed_until <= NOW()
		       THEN NULL ELSE intercity_committed_until::text END
		  FROM driver_profiles WHERE id = $1`, driverID).Scan(&v))
	return v
}

func insertTripForDriver(t *testing.T, ctx context.Context, driverID string, seats int, departIn string) string {
	t.Helper()
	corridor := intercityCorridor(t, ctx)
	var operatorID, vehicleID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT operator_id, vehicle_id FROM intercity_trips WHERE driver_id = $1 LIMIT 1`,
		driverID).Scan(&operatorID, &vehicleID))

	var tripID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_trips (
			operator_id, driver_id, vehicle_id, corridor, origin_name, destination_name,
			origin_point, destination_point, staging_address, depart_at,
			total_seats, price_per_seat_rwf)
		VALUES ($1, $2, $3, $4, 'Kigali', 'Musanze',
			ST_SetSRID(ST_MakePoint(30.06, -1.94), 4326)::geography,
			ST_SetSRID(ST_MakePoint(29.63, -1.49), 4326)::geography,
			'Nyabugogo', NOW() + $5::interval, $6, 3000)
		RETURNING id`, operatorID, driverID, vehicleID, corridor, departIn, seats).Scan(&tripID))

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE intercity_trips SET status='COMPLETED', held_seats=0, booked_seats=0 WHERE id=$1`, tripID)
	})
	return tripID
}

// The gate is only worth anything if real dispatch honours it. This exercises
// the ACTUAL FindNearby query used by the fallback dispatch path, not a
// paraphrase of it, and asserts the driver disappears and comes back.
func TestAvailability_FindNearbyExcludesCommittedDriver(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	tripID := insertIntercityTrip(t, ctx, 6)
	driverID := tripDriverID(t, ctx, tripID)

	// Put the driver online with a fresh fix at Nyabugogo so FindNearby can see
	// them at all.
	_, err := pool.Exec(ctx,
		`UPDATE driver_profiles SET is_online = TRUE WHERE id = $1`, driverID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO driver_locations (driver_id, location, updated_at)
		VALUES ($1, ST_SetSRID(ST_MakePoint(30.0619, -1.9441), 4326)::geography, NOW())
		ON CONFLICT (driver_id) DO UPDATE SET location = EXCLUDED.location, updated_at = NOW()`,
		driverID)
	require.NoError(t, err)

	visible := func() bool {
		found, err := driverRepo.FindNearby(ctx,
			geo.Point{Lat: -1.9441, Lng: 30.0619}, 5000, "LIGHT_HILUX", nil)
		require.NoError(t, err)
		for _, d := range found {
			if d.ProfileID == driverID {
				return true
			}
		}
		return false
	}

	require.True(t, visible(), "an uncommitted online driver must be dispatchable")

	enterLockoutWindow(t, ctx, tripID)
	require.NoError(t, repo.LockDriverForDeparture(ctx, tripID))
	require.False(t, visible(),
		"a driver 10 minutes from an intercity departure must NOT receive city offers")

	require.NoError(t, repo.SetTripStatus(ctx, tripID, intercity.TripCompleted))
	require.True(t, visible(),
		"and must be dispatchable again the moment the trip ends — anything else strands them")
}
