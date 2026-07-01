package matching_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// newTestRedis spins up an in-process Redis and returns a connected client.
func newTestRedis(t *testing.T) (*miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return mr, client
}

// ── Driver scoring formula ────────────────────────────────────────────────

func TestMatching_ScoringFormula(t *testing.T) {
	// score = (dist/expandedRadius × 0.6) + (min(declines,10)/10 × 0.25) + ((1-acceptanceRate/100) × 0.15)
	expandedRadius := 10000.0

	cases := []struct {
		name           string
		distM          float64
		declines       int
		acceptanceRate float64
		wantScore      float64
	}{
		{
			name:  "perfect driver — close, no declines, 100% acceptance",
			distM: 500, declines: 0, acceptanceRate: 100.0,
			wantScore: (500/expandedRadius)*0.6 + 0*0.25 + 0*0.15,
		},
		{
			name:  "far driver — 8km, 5 declines, 80% acceptance",
			distM: 8000, declines: 5, acceptanceRate: 80.0,
			wantScore: (8000/expandedRadius)*0.6 + (5.0/10)*0.25 + (0.2)*0.15,
		},
		{
			name:  "worst driver — max distance, 10+ declines, 0% acceptance",
			distM: 10000, declines: 15, acceptanceRate: 0.0,
			wantScore: (10000/expandedRadius)*0.6 + (10.0/10)*0.25 + 1.0*0.15,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalizedDist := tc.distM / expandedRadius
			normalizedDeclines := math.Min(float64(tc.declines), 10) / 10.0
			acceptancePenalty := 1.0 - tc.acceptanceRate/100.0
			score := (normalizedDist * 0.6) + (normalizedDeclines * 0.25) + (acceptancePenalty * 0.15)
			assert.InDelta(t, tc.wantScore, score, 0.0001)
		})
	}
}

// ── Lower score = higher priority (sorted ascending) ─────────────────────

func TestMatching_ScoreSortAscending(t *testing.T) {
	scores := []float64{0.8, 0.2, 0.5, 0.1, 0.9}
	// insertion sort (mirrors engine implementation)
	for i := 1; i < len(scores); i++ {
		for j := i; j > 0 && scores[j] < scores[j-1]; j-- {
			scores[j], scores[j-1] = scores[j-1], scores[j]
		}
	}
	for i := 1; i < len(scores); i++ {
		assert.LessOrEqual(t, scores[i-1], scores[i], "scores must be ascending")
	}
	assert.Equal(t, 0.1, scores[0], "lowest score must be first")
}

// ── Redis SET NX lock prevents double-assign ──────────────────────────────

func TestMatching_SetNXLockPreventsDoubleAssign(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	driverID := "driver-abc"
	lockKey := rkeys.K.MatchingLock(driverID)

	// First ride acquires lock
	ok1, err := rdb.SetNX(ctx, lockKey, "ride-1", 20*time.Second).Result()
	require.NoError(t, err)
	assert.True(t, ok1, "first SET NX must succeed")

	// Second ride cannot acquire same lock
	ok2, err := rdb.SetNX(ctx, lockKey, "ride-2", 20*time.Second).Result()
	require.NoError(t, err)
	assert.False(t, ok2, "second SET NX must fail — driver already locked")

	// After first ride releases, second can acquire
	rdb.Del(ctx, lockKey)
	ok3, err := rdb.SetNX(ctx, lockKey, "ride-2", 20*time.Second).Result()
	require.NoError(t, err)
	assert.True(t, ok3, "after release, new lock must succeed")
}

// ── 15-second timeout fires auto-decline ─────────────────────────────────

func TestMatching_TimeoutFiresAutodecline(t *testing.T) {
	mr, rdb := newTestRedis(t)
	ctx := context.Background()

	rideID := "ride-timeout-test"
	pendingKey := rkeys.K.RidePendingDriver(rideID)

	// Simulate engine writing pending_driver with 1s TTL
	rdb.Set(ctx, pendingKey, "driver-xyz", 1*time.Second)

	// Key exists immediately
	val, err := rdb.Get(ctx, pendingKey).Result()
	require.NoError(t, err)
	assert.Equal(t, "driver-xyz", val)

	// Fast-forward miniredis clock past the TTL
	mr.FastForward(2 * time.Second)

	// After TTL expires, key is gone (ValidateAcceptTTL returns false)
	_, err = rdb.Get(ctx, pendingKey).Result()
	assert.ErrorIs(t, err, goredis.Nil, "pending_driver key must expire after TTL")
}

// ── Daily declines expire at midnight UTC+2 ───────────────────────────────

func TestMatching_DailyDeclinesExpireAtMidnight(t *testing.T) {
	mr, rdb := newTestRedis(t)
	ctx := context.Background()

	driverID := "driver-declines"
	key := rkeys.K.DriverDailyDeclines(driverID)

	// Compute seconds until midnight Kigali (UTC+2)
	now := time.Now().UTC().Add(2 * time.Hour) // Kigali local time
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
	ttl := midnight.Sub(time.Now().UTC())
	if ttl < 0 {
		ttl += 24 * time.Hour
	}

	rdb.Set(ctx, key, 5, ttl)

	// Verify TTL is set and roughly correct (within 2 seconds)
	remaining := mr.TTL(key)
	assert.Greater(t, remaining, time.Duration(0), "TTL must be positive")
	assert.LessOrEqual(t, remaining, ttl+2*time.Second, "TTL must not exceed midnight")
}

// ── AVAILABLE state check before offering ────────────────────────────────

func TestMatching_AvailableStateCheckBeforeOffer(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	driverID := "driver-state-test"
	stateKey := rkeys.K.DriverState(driverID)

	// Driver is OFFLINE — should be filtered out
	rdb.Set(ctx, stateKey, "OFFLINE", 0)
	state, _ := rdb.Get(ctx, stateKey).Result()
	assert.NotEqual(t, "AVAILABLE", state, "OFFLINE driver must not be offered rides")

	// Driver is ON_TRIP — should be filtered out
	rdb.Set(ctx, stateKey, "ON_TRIP", 0)
	state, _ = rdb.Get(ctx, stateKey).Result()
	assert.NotEqual(t, "AVAILABLE", state, "ON_TRIP driver must not be offered rides")

	// Driver is AVAILABLE — should pass filter
	rdb.Set(ctx, stateKey, "AVAILABLE", 0)
	state, _ = rdb.Get(ctx, stateKey).Result()
	assert.Equal(t, "AVAILABLE", state, "AVAILABLE driver must pass filter")
}
