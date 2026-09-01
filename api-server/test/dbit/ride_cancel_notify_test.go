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
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// Regression coverage for the pre-match cancel FCM gap: a customer cancelling
// while still SEARCHING used to dismiss every offered driver over WebSocket
// only, so a driver whose app was backgrounded (or reconnecting, or on a
// second device — the hub is single-socket-per-identity) never learned the
// offer was gone and the request popup lingered. CancelRide must now push an
// FCM notification to every pending driver alongside the WS send.
//
// SendToAllDevices always persists an in-app "notifications" row before
// attempting the device push (see notification.Service.Persist), so a row
// landing for each pending driver's user is the same proxy the rest of this
// suite uses (see TestNotifyDriver_TargetsOnlyThatDriver) to prove the FCM
// path fired without needing a live Firebase credential.
func newTestRideServiceWithNotify(t *testing.T) (*ride.Service, *ride.Repository, *goredis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	hub := tracking.NewHub(rdb, zerolog.Nop())
	t.Cleanup(func() { hub.Close() })

	notifySvc := notification.New(&config.Config{}, zerolog.Nop())
	notifySvc.SetRepository(notification.NewRepository(pool))

	// CancelRide unconditionally calls s.analytics.Publish (ride.cancelled),
	// which immediately dereferences the receiver (s.db, s.log) with no nil
	// guard — a nil *analytics.Service here is not "no analytics", it's a nil
	// pointer dereference panic the instant CancelRide runs. Every other
	// caller of ride.NewService (main.go) always passes a real one; give this
	// service a real, test-DB-backed one too, same as
	// newDriverServiceWithRedis does for driver.Service in
	// driver_vehicle_approval_test.go.
	ana := analytics.NewService(pool, rdb, zerolog.Nop())

	repo := ride.NewRepository(pool)
	svc := ride.NewService(repo, rdb, notifySvc, ana, hub, &config.Config{}, zerolog.Nop())
	return svc, repo, rdb
}

func TestCancelRide_PreMatch_NotifiesPendingDriversViaFCM(t *testing.T) {
	ctx := context.Background()
	svc, repo, rdb := newTestRideServiceWithNotify(t)
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)

	driver1, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-drv1", "android", nil, nil)
	require.NoError(t, err)
	driver1Profile := insertDriverProfile(t, ctx, driver1.ID, "MOTO_BIKE")

	driver2, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-drv2", "android", nil, nil)
	require.NoError(t, err)
	driver2Profile := insertDriverProfile(t, ctx, driver2.ID, "MOTO_BIKE")

	rideID := createTestRide(t, ctx, repo, customer.ID, "", ride.StatusSearching)

	// Seed the offer batch the same way the matching engine does mid-search.
	require.NoError(t, rdb.SAdd(ctx, rkeys.K.RidePendingDrivers(rideID), driver1Profile, driver2Profile).Err())

	require.NoError(t, svc.CancelRide(ctx, rideID, customer.ID, "changed my mind"))

	for _, drv := range []*auth.User{driver1, driver2} {
		var count int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'ride' AND title = $2`,
			drv.ID, "Ride no longer available").Scan(&count))
		require.Equal(t, 1, count, "driver %s must receive an FCM-backed notification when the offer is dismissed", drv.ID)
	}
}

// A cancel with no pending offers (nobody was ever offered the ride) must not
// error — the empty-set loop path (no pending drivers to dismiss) is a no-op.
func TestCancelRide_PreMatch_NoPendingDrivers_Succeeds(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newTestRideServiceWithNotify(t)
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust2", "android", nil, nil)
	require.NoError(t, err)

	rideID := createTestRide(t, ctx, repo, customer.ID, "", ride.StatusSearching)

	require.NoError(t, svc.CancelRide(ctx, rideID, customer.ID, "changed my mind"))
}
