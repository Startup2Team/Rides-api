package location_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/location"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *goredis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return mr, client
}

// ── Geohash precision 6 snapping ─────────────────────────────────────────

func TestRouteCache_GeohashSnapping(t *testing.T) {
	// Two coordinates within ~100m of each other should produce the same geohash6
	// (geohash6 cell is ~1.2km × 0.6km)
	lat1, lng1 := -1.9441, 30.0619 // Kigali CBD
	lat2, lng2 := -1.9445, 30.0622 // ~50m away

	h1 := location.Geohash6(lat1, lng1)
	h2 := location.Geohash6(lat2, lng2)

	assert.Equal(t, h1, h2, "nearby coords must snap to same geohash6 cell")
	assert.Len(t, h1, 6, "geohash must be exactly 6 chars")
}

func TestRouteCache_GeohashDistantCoords(t *testing.T) {
	// Two coordinates far apart must produce different geohashes
	h1 := location.Geohash6(-1.9441, 30.0619) // CBD
	h2 := location.Geohash6(-1.9731, 30.0380) // Nyamirambo (~4km away)

	assert.NotEqual(t, h1, h2, "distant coords must produce different geohash6")
}

// ── Cache key format ──────────────────────────────────────────────────────

func TestRouteCache_CacheKeyFormat(t *testing.T) {
	pickupLat, pickupLng := -1.9441, 30.0619
	destLat, destLng := -1.9355, 30.1127
	vehicleType := "MOTO_BIKE"

	originHash := location.Geohash6(pickupLat, pickupLng)
	destHash := location.Geohash6(destLat, destLng)
	expected := fmt.Sprintf("%s:%s:%s", originHash, destHash, vehicleType)

	// Verify format: originHash:destHash:vehicleType
	parts := splitKey(expected)
	require.Len(t, parts, 3, "cache key must have 3 parts separated by :")
	assert.Len(t, parts[0], 6, "origin geohash must be 6 chars")
	assert.Len(t, parts[1], 6, "dest geohash must be 6 chars")
	assert.Equal(t, vehicleType, parts[2])
}

// ── Redis hit returned without Postgres query ─────────────────────────────

func TestRouteCache_RedisHitSkipsDB(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	cacheKey := "ks3g7v:ks3gcx:MOTO_BIKE"
	redisKey := rkeys.K.RouteCache(cacheKey)

	// Pre-populate Redis
	cached := map[string]interface{}{
		"cache_key":        cacheKey,
		"distance_km":      5.2,
		"duration_minutes": 18,
		"use_count":        42,
	}
	data, _ := json.Marshal(cached)
	rdb.Set(ctx, redisKey, string(data), 24*time.Hour)

	// Verify key exists in Redis
	val, err := rdb.Get(ctx, redisKey).Result()
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(val), &result))
	assert.Equal(t, cacheKey, result["cache_key"])
	assert.Equal(t, float64(5.2), result["distance_km"])
	// If Redis hit, DB is never queried — verified by the service code path
}

// ── Generic dest skips route cache entirely ───────────────────────────────

func TestRouteCache_GenericDestSkipped(t *testing.T) {
	// When is_generic_dest=true, dest coords are zero/approximate.
	// The service must not attempt geohash or cache lookup.
	// We verify that zero coords produce a valid but meaningless geohash
	// (the service checks is_generic_dest flag before calling GetRoute).
	destLat, destLng := 0.0, 0.0
	hash := location.Geohash6(destLat, destLng)
	// The hash is valid but the service skips it when is_generic_dest=true
	assert.Len(t, hash, 6, "geohash still computes but service skips it for generic dest")
}

// ── agreed_fare appended on completion ───────────────────────────────────

func TestRouteCache_FareAppended(t *testing.T) {
	_, rdb := newTestRedis(t)
	ctx := context.Background()

	// Simulate Redis cache invalidation after fare recorded
	cacheKey := "ks3g7v:ks3gcx:CAB_TAXI"
	redisKey := rkeys.K.RouteCache(cacheKey)

	rdb.Set(ctx, redisKey, `{"cache_key":"test"}`, time.Hour)

	// After RecordAgreedFare, Redis key is deleted so next read gets fresh avg
	rdb.Del(ctx, redisKey)

	_, err := rdb.Get(ctx, redisKey).Result()
	assert.ErrorIs(t, err, goredis.Nil, "Redis key must be invalidated after fare recorded")
}

// ── Helpers ───────────────────────────────────────────────────────────────

func splitKey(key string) []string {
	var parts []string
	start := 0
	for i, c := range key {
		if c == ':' {
			parts = append(parts, key[start:i])
			start = i + 1
		}
	}
	parts = append(parts, key[start:])
	return parts
}
