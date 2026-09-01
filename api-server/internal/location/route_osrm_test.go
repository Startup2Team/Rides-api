package location

// White-box tests (package location, not location_test) for the pure OSRM
// wiring helpers that don't touch Postgres/Redis: the Haversine fallback
// formula, the OSRM→RouteResult conversion, and the RoutingEnabled feature
// gate. DB/Redis-touching paths (GetRoute's cache-miss → OSRM → route_cache
// write) are covered by the routing.Client tests (internal/routing) plus
// staging/integration verification — see the task report.

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/routing"
)

// ── RoutingEnabled: the feature-flag gate ───────────────────────────────────

func TestService_RoutingEnabled_NoRouterConfigured(t *testing.T) {
	svc := NewService(nil, nil, &config.Config{}, zerolog.Nop())
	assert.False(t, svc.RoutingEnabled(), "a Service that never called SetRoutingClient must report routing disabled")
}

func TestService_RoutingEnabled_OSRMURLEmpty(t *testing.T) {
	svc := NewService(nil, nil, &config.Config{}, zerolog.Nop())
	cfg := &config.Config{}
	cfg.Routing.OSRMURL = ""
	svc.SetRoutingClient(routing.New(cfg, zerolog.Nop()))
	assert.False(t, svc.RoutingEnabled(), "OSRM_URL unset must keep routing disabled — this is the byte-for-byte-off guarantee")
}

func TestService_RoutingEnabled_OSRMURLSet(t *testing.T) {
	svc := NewService(nil, nil, &config.Config{}, zerolog.Nop())
	cfg := &config.Config{}
	cfg.Routing.OSRMURL = "http://osrm.local:5000"
	svc.SetRoutingClient(routing.New(cfg, zerolog.Nop()))
	assert.True(t, svc.RoutingEnabled())
}

// ── haversineEstimate: today's pre-OSRM fallback, unchanged ────────────────

func TestHaversineEstimate_KnownCorridor(t *testing.T) {
	// Kigali CBD -> Kimironko, ~7.3km straight-line.
	pickupLat, pickupLng := -1.9441, 30.0619
	destLat, destLng := -1.9355, 30.1127

	straightKM, estimatedKM, estimatedMin := haversineEstimate(pickupLat, pickupLng, destLat, destLng)

	require.Greater(t, straightKM, 0.0)
	// estimatedKM must be exactly straightKM * 1.25 (the documented road-factor) —
	// pinning this guards against silently changing the fallback formula.
	assert.InDelta(t, straightKM*1.25, estimatedKM, 1e-9)
	// estimatedMin = estimatedKM/30*60 + 3, truncated to int.
	wantMin := int(estimatedKM/30*60) + 3
	assert.Equal(t, wantMin, estimatedMin)
}

func TestHaversineEstimate_SameOriginAndDest_FixedOverheadFloor(t *testing.T) {
	// Zero distance still costs the +3 min fixed overhead — the formula's
	// floor only kicks in if that overhead were ever removed/negative.
	_, estimatedKM, estimatedMin := haversineEstimate(-1.94, 30.06, -1.94, 30.06)
	assert.Equal(t, 0.0, estimatedKM)
	assert.Equal(t, 3, estimatedMin)
}

// ── osrmRouteToResult: OSRM route -> cached RouteResult, safely ────────────

func TestOSRMRouteToResult_TypicalRoute(t *testing.T) {
	rr := routing.RouteResult{DistanceMeters: 5230.4, DurationSec: 612.8, Geometry: "abc123"}
	result := osrmRouteToResult("key1", "geo1", "geo2", rr)

	assert.Equal(t, "key1", result.CacheKey)
	assert.InDelta(t, 5.2304, result.DistanceKM, 1e-9)
	require.NotNil(t, result.DurationSeconds)
	assert.Equal(t, 613, *result.DurationSeconds, "613.3 rounds to 613")
	assert.Equal(t, 11, result.DurationMinutes, "613s = 10m13s, rounds UP to 11 minutes")
	require.NotNil(t, result.Geometry)
	assert.Equal(t, "abc123", *result.Geometry)
}

func TestOSRMRouteToResult_EmptyGeometry_BecomesNil(t *testing.T) {
	rr := routing.RouteResult{DistanceMeters: 1000, DurationSec: 90, Geometry: ""}
	result := osrmRouteToResult("key1", "geo1", "geo2", rr)

	assert.Nil(t, result.Geometry, "an empty OSRM geometry must not be stored as a meaningless empty-string polyline")
}

func TestOSRMRouteToResult_ZeroDurationRoute_FloorsAtOneMinute(t *testing.T) {
	// origin == destination: OSRM can legitimately return distance=0, duration=0.
	rr := routing.RouteResult{DistanceMeters: 0, DurationSec: 0, Geometry: ""}
	result := osrmRouteToResult("key1", "geo1", "geo2", rr)

	assert.Equal(t, 0.0, result.DistanceKM)
	require.NotNil(t, result.DurationSeconds)
	assert.Equal(t, 0, *result.DurationSeconds)
	assert.Equal(t, 1, result.DurationMinutes, "a same-point route must still floor at 1 minute, matching the Haversine fallback's floor")
}

func TestOSRMRouteToResult_ExactMinuteBoundary_NoOverRound(t *testing.T) {
	// Exactly 120 seconds must be exactly 2 minutes, not 3.
	rr := routing.RouteResult{DistanceMeters: 2000, DurationSec: 120, Geometry: "xyz"}
	result := osrmRouteToResult("key1", "geo1", "geo2", rr)

	assert.Equal(t, 2, result.DurationMinutes)
}
