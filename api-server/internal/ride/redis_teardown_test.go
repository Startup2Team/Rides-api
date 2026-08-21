package ride

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// ── releaseRideRedisState — live-location PII teardown ────────────────────
//
// Security review (F2): RideCustomerLocation/RideDriverLocation were only
// ever cleaned up by their 30-min TTL, leaving a completed/cancelled ride's
// GPS trail sitting in Redis long after the ride ended. releaseRideRedisState
// now explicitly deletes both, in addition to the keys it already released.

func TestReleaseRideRedisState_DeletesLocationPII(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	s := &Service{redis: rdb, log: zerolog.Nop()}
	ctx := context.Background()

	const (
		rideID      = "ride-1"
		customerID  = "customer-1"
		vehicleType = "MOTO_BIKE"
	)
	driverProfileID := "driver-profile-1"

	// Seed every key releaseRideRedisState is responsible for, including the
	// two location caches, as if the ride were mid-trip.
	require.NoError(t, rdb.Set(ctx, rkeys.K.CustomerActiveRide(customerID), rideID, 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideState(rideID), "IN_PROGRESS", 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideCustomerLocation(rideID), `{"lat":-1.94,"lng":30.06}`, 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideDriverLocation(rideID), `{"lat":-1.95,"lng":30.07}`, 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide(driverProfileID), rideID, 0).Err())

	s.releaseRideRedisState(ctx, rideID, customerID, &driverProfileID, vehicleType)

	for _, key := range []string{
		rkeys.K.CustomerActiveRide(customerID),
		rkeys.K.RideState(rideID),
		rkeys.K.RideCustomerLocation(rideID),
		rkeys.K.RideDriverLocation(rideID),
		rkeys.K.DriverActiveRide(driverProfileID),
	} {
		exists, err := rdb.Exists(ctx, key).Result()
		require.NoError(t, err)
		assert.Zero(t, exists, "key %q must be deleted", key)
	}
}

func TestReleaseRideRedisState_NoDriver_StillClearsLocationPII(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	s := &Service{redis: rdb, log: zerolog.Nop()}
	ctx := context.Background()

	const (
		rideID     = "ride-2"
		customerID = "customer-2"
	)

	// A ride cancelled before a driver was ever assigned can still have a
	// customer_location entry (the customer publishes as soon as matching
	// starts, in some client flows) — must be cleared even with a nil driver.
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideCustomerLocation(rideID), `{"lat":0,"lng":0}`, 0).Err())

	s.releaseRideRedisState(ctx, rideID, customerID, nil, "MOTO_BIKE")

	exists, err := rdb.Exists(ctx, rkeys.K.RideCustomerLocation(rideID)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
}
