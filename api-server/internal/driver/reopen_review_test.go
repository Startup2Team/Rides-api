package driver

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// evictOnlineDriverFromRedis is the Redis half of reopenForReview's
// force-offline eviction (LOW-1): when a document upload reopens review for
// an APPROVED driver, they must not keep a ghost pin in the customer
// nearby-map / matching engine's Redis geo index just because their approval
// (Postgres) no longer describes what's on file. reopenForReview itself
// can't be unit-tested — it also writes through *Repository, which is tied
// directly to *pgxpool.Pool (see test/dbit/driver_kyc_resubmit_test.go for
// the Postgres-backed end-to-end coverage of the full transition, including
// this method's sibling repo.UpdateOnlineStatus write) — but the Redis
// eviction was split out specifically so it has isolated coverage here.
func TestEvictOnlineDriverFromRedis_ClearsStateAndGeoIndex(t *testing.T) {
	s, rdb := newTestService(t)
	ctx := context.Background()

	// Seed the driver as online: a Redis DriverState entry and a pin in the
	// geo index for their transport type — exactly what SetAvailability
	// would have written while they were APPROVED and online.
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverState("driver-1"), "ONLINE", 0).Err())
	require.NoError(t, rdb.GeoAdd(ctx, rkeys.K.DriverGeoIndex("MOTO_BIKE"), &goredis.GeoLocation{
		Name: "driver-1", Longitude: 30.0619, Latitude: -1.9441,
	}).Err())
	// A different driver in the same geo index must be untouched.
	require.NoError(t, rdb.GeoAdd(ctx, rkeys.K.DriverGeoIndex("MOTO_BIKE"), &goredis.GeoLocation{
		Name: "driver-2", Longitude: 30.0, Latitude: -1.9,
	}).Err())

	s.evictOnlineDriverFromRedis(ctx, "driver-1", "MOTO_BIKE")

	state, err := rdb.Get(ctx, rkeys.K.DriverState("driver-1")).Result()
	require.NoError(t, err)
	assert.Equal(t, "OFFLINE", state, "reopening review from APPROVED must force the Redis driver state offline")

	_, err = rdb.ZScore(ctx, rkeys.K.DriverGeoIndex("MOTO_BIKE"), "driver-1").Result()
	assert.ErrorIs(t, err, goredis.Nil, "the evicted driver must no longer be a member of the geo index")

	score, err := rdb.ZScore(ctx, rkeys.K.DriverGeoIndex("MOTO_BIKE"), "driver-2").Result()
	require.NoError(t, err, "an unrelated driver in the same geo index must not be evicted")
	assert.NotZero(t, score)
}

func TestEvictOnlineDriverFromRedis_NilRedisClient_NoOp(t *testing.T) {
	// main.go always wires Redis, but reopenForReview's own dbit coverage
	// (test/dbit/driver_kyc_resubmit_test.go) constructs the driver.Service
	// with a nil rdb — this must be a safe no-op there, not a panic on a nil
	// interface method call.
	s := &Service{redis: nil, log: zerolog.Nop()}

	assert.NotPanics(t, func() {
		s.evictOnlineDriverFromRedis(context.Background(), "driver-1", "MOTO_BIKE")
	})
}
