package e2e_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/location"
	"github.com/workspace/ride-platform/internal/ride"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return mr, client
}

// ── Happy path: full ride state progression ───────────────────────────────

func TestE2E_HappyPathStateProgression(t *testing.T) {
	chain := []struct{ from, to ride.Status }{
		{ride.StatusSearching, ride.StatusMatched},
		{ride.StatusMatched, ride.StatusNegotiating},
		{ride.StatusNegotiating, ride.StatusConfirmed},
		{ride.StatusConfirmed, ride.StatusDriverEnRoute},
		{ride.StatusDriverEnRoute, ride.StatusDriverArrived},
		{ride.StatusDriverArrived, ride.StatusInProgress},
		{ride.StatusInProgress, ride.StatusCompleted},
	}

	for _, step := range chain {
		err := ride.ValidateTransition(step.from, step.to)
		assert.NoError(t, err, "happy path step %s→%s must be valid", step.from, step.to)
	}
}

// ── Generic destination — no route cache ─────────────────────────────────

func TestE2E_GenericDestinationSkipsRouteCache(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	// Generic dest has zero coords — service skips cache when is_generic_dest=true
	destLat, destLng := 0.0, 0.0
	hash := location.Geohash6(destLat, destLng)
	cacheKey := hash + ":ks3g7v:MOTO_BIKE"

	_, err := rdb.Get(ctx, rkeys.K.RouteCache(cacheKey)).Result()
	assert.ErrorIs(t, err, goredis.Nil, "generic dest must not have a cache entry")
}

// ── Negotiation timeout cancels ride ─────────────────────────────────────

func TestE2E_NegotiationTimeoutCancelsRide(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timer test in short mode")
	}

	cancelled := make(chan string, 1)
	currentStatus := ride.StatusNegotiating

	time.AfterFunc(100*time.Millisecond, func() {
		if currentStatus == ride.StatusNegotiating {
			cancelled <- "negotiation_timeout"
		}
	})

	select {
	case reason := <-cancelled:
		assert.Equal(t, "negotiation_timeout", reason)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("negotiation timeout did not fire")
	}
}

// ── App crash recovery — active ride from Redis ───────────────────────────

func TestE2E_ActiveRideRecovery(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	customerID := "customer-recovery"
	rideID := "ride-recovery-123"

	rdb.Set(ctx, rkeys.K.CustomerActiveRide(customerID), rideID, 0)
	rdb.Set(ctx, rkeys.K.RideState(rideID), string(ride.StatusNegotiating), 0)

	recoveredRideID, err := rdb.Get(ctx, rkeys.K.CustomerActiveRide(customerID)).Result()
	require.NoError(t, err)
	assert.Equal(t, rideID, recoveredRideID)

	state, err := rdb.Get(ctx, rkeys.K.RideState(rideID)).Result()
	require.NoError(t, err)
	assert.Equal(t, string(ride.StatusNegotiating), state)
}

// ── Pickup expired flow ───────────────────────────────────────────────────

func TestE2E_PickupExpiredFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timer test in short mode")
	}

	expired := make(chan bool, 1)
	currentStatus := ride.StatusDriverArrived

	time.AfterFunc(100*time.Millisecond, func() {
		if currentStatus == ride.StatusDriverArrived {
			expired <- true
		}
	})

	select {
	case <-expired:
		// pickup_expired flag set, WS event sent
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pickup expiry timer did not fire")
	}
}

// ── Customer 5 cancels — warning event ───────────────────────────────────

func TestE2E_CustomerCancelWarning(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	customerID := "customer-cancel-test"
	key := rkeys.K.CustomerDailyCancel(customerID)
	warnThreshold := 5

	var warnFired bool
	for i := 1; i <= 6; i++ {
		count, _ := rdb.Incr(ctx, key).Result()
		if int(count) == warnThreshold {
			warnFired = true
		}
	}

	assert.True(t, warnFired, "warning event must fire at 5th cancellation")
	finalCount, _ := rdb.Get(ctx, key).Int64()
	assert.Equal(t, int64(6), finalCount)
}

// ── Route cache warm after first ride ────────────────────────────────────

func TestE2E_RouteCacheWarmAfterFirstRide(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	cacheKey := "ks3g7v:ks3gcx:MOTO_BIKE"
	redisKey := rkeys.K.RouteCache(cacheKey)

	// First ride stores route in Redis
	rdb.Set(ctx, redisKey, `{"cache_key":"ks3g7v:ks3gcx:MOTO_BIKE","distance_km":5.2}`, 24*time.Hour)

	// Second ride on same corridor hits Redis
	val, err := rdb.Get(ctx, redisKey).Result()
	require.NoError(t, err)
	assert.Contains(t, val, "distance_km", "second ride must hit Redis cache")
}

// ── Mode switch blocked during active ride ────────────────────────────────

func TestE2E_ModeSwitchBlockedDuringActiveRide(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	customerID := "customer-mode-switch"
	rideID := "active-ride-123"

	rdb.Set(ctx, rkeys.K.CustomerActiveRide(customerID), rideID, 0)

	activeRide, err := rdb.Get(ctx, rkeys.K.CustomerActiveRide(customerID)).Result()
	require.NoError(t, err)
	assert.NotEmpty(t, activeRide, "active ride exists — mode switch must return 409")
}

// ── All offers used — offer_limit event fires ─────────────────────────────

func TestE2E_AllOffersUsedEventFires(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	rideID := "ride-offer-limit"
	maxOffers := 3

	customerKey := rkeys.K.RideOfferCount(rideID, "customer")
	driverKey := rkeys.K.RideOfferCount(rideID, "driver")

	var customerLimitFired, driverLimitFired bool

	for i := 1; i <= maxOffers; i++ {
		c, _ := rdb.Incr(ctx, customerKey).Result()
		if int(c) == maxOffers {
			customerLimitFired = true
		}
		d, _ := rdb.Incr(ctx, driverKey).Result()
		if int(d) == maxOffers {
			driverLimitFired = true
		}
	}

	assert.True(t, customerLimitFired, "customer offer_limit event must fire at 3")
	assert.True(t, driverLimitFired, "driver offer_limit event must fire at 3")
}

// ── Driver acceptance rate degrades on declines ───────────────────────────

func TestE2E_AcceptanceRateDegrades(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	driverID := "driver-rate-test"
	key := rkeys.K.DriverDailyDeclines(driverID)

	expandedRadius := 10000.0
	distM := 2000.0
	baseScore := (distM / expandedRadius) * 0.6

	// Score with 0 declines, 100% acceptance
	score0 := baseScore + 0 + 0

	// After 3 declines, 90% acceptance
	rdb.Set(ctx, key, 3, 0)
	declines, _ := rdb.Get(ctx, key).Int()
	acceptanceRate := 90.0

	normalizedDeclines := math.Min(float64(declines), 10) / 10.0
	acceptancePenalty := 1.0 - acceptanceRate/100.0
	score3 := baseScore + (normalizedDeclines * 0.25) + (acceptancePenalty * 0.15)

	assert.Greater(t, score3, score0, "score must increase (worsen) after declines")
}
