//go:build integration

package dbit

import (
	"context"
	"testing"

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
	"github.com/workspace/ride-platform/pkg/geo"
)

// Regression coverage for server-side auto-arrival: MarkDriverArrivedIfNear
// was dead code (MarkDriverArrived had zero callers) until it was wired into
// the driver location-update path. Before this fix a driver who reached the
// pickup point but never tapped "Arrived" left the ride stuck in
// DRIVER_EN_ROUTE forever. These tests exercise the real Postgres transition
// + FCM path (notifications row), not just the pure distance math.

// createdTestPickup mirrors createTestRide's fixed pickup point
// (test/dbit/customer_location_test.go) so distance offsets here are
// meaningful against the row that fixture actually writes.
var arrivalTestPickup = geo.Point{Lat: -1.9441, Lng: 30.0619}

func newTestRideServiceWithArrivalRadius(t *testing.T, radiusM int, devSkipGeofence bool) (*ride.Service, *ride.Repository) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	hub := tracking.NewHub(rdb, zerolog.Nop())
	t.Cleanup(func() { hub.Close() })

	notifySvc := notification.New(&config.Config{}, zerolog.Nop())
	notifySvc.SetRepository(notification.NewRepository(pool))
	ana := analytics.NewService(pool, rdb, zerolog.Nop())

	cfg := &config.Config{}
	cfg.Ride.ArrivalRadiusM = radiusM
	cfg.Ride.DevSkipGeofence = devSkipGeofence

	repo := ride.NewRepository(pool)
	svc := ride.NewService(repo, rdb, notifySvc, ana, hub, cfg, zerolog.Nop())
	return svc, repo
}

func setupEnRouteRide(t *testing.T, ctx context.Context, repo *ride.Repository) (rideID, driverProfileID, customerID string) {
	t.Helper()
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-driver", "android", nil, nil)
	require.NoError(t, err)
	profileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	rideID = createTestRide(t, ctx, repo, customer.ID, profileID, ride.StatusDriverEnRoute)
	return rideID, profileID, customer.ID
}

func TestMarkDriverArrivedIfNear_WithinRadius_TransitionsAndNotifies(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceWithArrivalRadius(t, 75, false)
	rideID, driverProfileID, customerID := setupEnRouteRide(t, ctx, repo)

	// ~22m north of pickup — inside the 75m default radius.
	near := geo.Point{Lat: arrivalTestPickup.Lat + 0.0002, Lng: arrivalTestPickup.Lng}
	require.Less(t, geo.DistanceKM(near, arrivalTestPickup)*1000, 75.0, "test fixture: near point must actually be inside the radius")

	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, near))

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusDriverArrived, r.Status)

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'ride' AND title = $2`,
		customerID, "Driver arrived").Scan(&count))
	require.Equal(t, 1, count, "customer must receive an FCM-backed notification when auto-arrival fires")
}

func TestMarkDriverArrivedIfNear_OutsideRadius_NoOp(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceWithArrivalRadius(t, 75, false)
	rideID, driverProfileID, _ := setupEnRouteRide(t, ctx, repo)

	// ~1.1km north of pickup — well outside the radius.
	far := geo.Point{Lat: arrivalTestPickup.Lat + 0.01, Lng: arrivalTestPickup.Lng}
	require.Greater(t, geo.DistanceKM(far, arrivalTestPickup)*1000, 75.0, "test fixture: far point must actually be outside the radius")

	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, far))

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusDriverEnRoute, r.Status, "a ping outside the geofence must not transition the ride")
}

func TestMarkDriverArrivedIfNear_WrongDriver_NoOp(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceWithArrivalRadius(t, 75, false)
	rideID, _, _ := setupEnRouteRide(t, ctx, repo)

	// A second, unrelated driver's ping against someone else's ride ID must
	// never transition it — MarkDriverArrivedIfNear re-checks driver identity
	// against Postgres, not the caller's claim.
	strangerUser, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-stranger", "android", nil, nil)
	require.NoError(t, err)
	strangerProfileID := insertDriverProfile(t, ctx, strangerUser.ID, "MOTO_BIKE")

	near := geo.Point{Lat: arrivalTestPickup.Lat + 0.0002, Lng: arrivalTestPickup.Lng}
	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, strangerProfileID, near))

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusDriverEnRoute, r.Status)
}

func TestMarkDriverArrivedIfNear_AlreadyArrived_IdempotentNoError(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceWithArrivalRadius(t, 75, false)
	rideID, driverProfileID, customerID := setupEnRouteRide(t, ctx, repo)

	near := geo.Point{Lat: arrivalTestPickup.Lat + 0.0002, Lng: arrivalTestPickup.Lng}
	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, near))
	// A second ping (or a concurrent one that raced the first) must be a
	// silent no-op — not an error surfaced from the location-update endpoint —
	// and must not double-notify the customer.
	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, near))

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'ride' AND title = $2`,
		customerID, "Driver arrived").Scan(&count))
	require.Equal(t, 1, count, "a repeat ping after arrival must not send a second notification")
}

func TestMarkDriverArrivedIfNear_DevSkipGeofence_BypassesDistance(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestRideServiceWithArrivalRadius(t, 75, true)
	rideID, driverProfileID, _ := setupEnRouteRide(t, ctx, repo)

	far := geo.Point{Lat: arrivalTestPickup.Lat + 0.01, Lng: arrivalTestPickup.Lng}
	require.NoError(t, svc.MarkDriverArrivedIfNear(ctx, rideID, driverProfileID, far))

	r, err := repo.FindByID(ctx, rideID)
	require.NoError(t, err)
	require.Equal(t, ride.StatusDriverArrived, r.Status, "DevSkipGeofence must bypass distance the same way withinRadius does for the manual button")
}
