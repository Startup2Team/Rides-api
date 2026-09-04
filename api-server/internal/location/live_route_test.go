package location

// White-box tests (package location) for the pure live-route conversion
// helper. DB/Redis-touching paths (GetLiveRoute -> GetRoute -> route_cache)
// are exercised by GetRoute's own coverage plus staging/integration
// verification — see the task report; toLiveRouteResult is what's safe and
// meaningful to unit-test without Postgres/Redis.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── parseCoordParam ──────────────────────────────────────────────────────

func TestParseCoordParam_Valid(t *testing.T) {
	f, err := parseCoordParam("-1.9441", -90, 90)
	require.NoError(t, err)
	assert.InDelta(t, -1.9441, f, 1e-9)
}

func TestParseCoordParam_Missing(t *testing.T) {
	_, err := parseCoordParam("", -90, 90)
	assert.Error(t, err)
}

func TestParseCoordParam_NotANumber(t *testing.T) {
	_, err := parseCoordParam("abc", -90, 90)
	assert.Error(t, err)
}

func TestParseCoordParam_OutOfRange(t *testing.T) {
	_, err := parseCoordParam("91", -90, 90)
	assert.Error(t, err, "latitude beyond 90 must be rejected")

	_, err = parseCoordParam("-181", -180, 180)
	assert.Error(t, err, "longitude beyond -180 must be rejected")
}

// ── LiveRoute handler: validation short-circuits before touching Service ──

func TestHandler_LiveRoute_MissingCoordinate_400(t *testing.T) {
	h := NewHandler(nil, nil) // never dereferenced: validation fails first
	req := httptest.NewRequest(http.MethodGet, "/api/v1/locations/live-route?origin_lat=-1.9&origin_lng=30.0&dest_lat=-1.95", nil)
	w := httptest.NewRecorder()

	h.LiveRoute(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_LiveRoute_OutOfRangeCoordinate_400(t *testing.T) {
	h := NewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/locations/live-route?origin_lat=-1.9&origin_lng=200&dest_lat=-1.95&dest_lng=30.1", nil)
	w := httptest.NewRecorder()

	h.LiveRoute(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestToLiveRouteResult_NilRoute_Unavailable(t *testing.T) {
	// Total cache miss with no OSRM (or OSRM disabled): GetRoute returns nil.
	result := toLiveRouteResult(nil)

	assert.False(t, result.Available)
	assert.Nil(t, result.DistanceMeters)
	assert.Nil(t, result.DurationSeconds)
	assert.Nil(t, result.Geometry)
}

func TestToLiveRouteResult_PreOSRMCacheHit_Unavailable(t *testing.T) {
	// A route_cache row written by UpsertRoute (client-supplied) or
	// WarmLandmarkRoutes (Haversine warm-up) has distance/duration but no
	// duration_seconds/geometry — this must NOT be reported as a real route,
	// or the client would draw a fabricated straight-line "road".
	r := &RouteResult{
		CacheKey: "abc:def:MOTO_BIKE", DistanceKM: 5.2, DurationMinutes: 18,
		// DurationSeconds and Geometry left nil.
	}
	result := toLiveRouteResult(r)

	assert.False(t, result.Available, "a pre-OSRM cache hit must not report available=true")
	assert.Nil(t, result.DistanceMeters)
	assert.Nil(t, result.Geometry)
}

func TestToLiveRouteResult_RealOSRMRoute_Available(t *testing.T) {
	durationSec := 613
	geometry := "abc123"
	r := &RouteResult{
		CacheKey: "abc:def:MOTO_BIKE", DistanceKM: 5.2304,
		DurationMinutes: 11, DurationSeconds: &durationSec, Geometry: &geometry,
	}
	result := toLiveRouteResult(r)

	require.True(t, result.Available)
	require.NotNil(t, result.DistanceMeters)
	assert.InDelta(t, 5230.4, *result.DistanceMeters, 1e-6)
	require.NotNil(t, result.DurationSeconds)
	assert.Equal(t, 613, *result.DurationSeconds)
	require.NotNil(t, result.Geometry)
	assert.Equal(t, "abc123", *result.Geometry)
}

func TestToLiveRouteResult_RealOSRMRoute_EmptyGeometryStaysNil(t *testing.T) {
	// Origin == destination: OSRM can legitimately return a real route with
	// distance=0/duration flooring at 1 minute and no polyline (see
	// osrmRouteToResult). Available must still be true — DurationSeconds is
	// the signal, not Geometry — but Geometry stays nil, not "".
	durationSec := 0
	r := &RouteResult{
		CacheKey: "abc:abc:MOTO_BIKE", DistanceKM: 0,
		DurationMinutes: 1, DurationSeconds: &durationSec, Geometry: nil,
	}
	result := toLiveRouteResult(r)

	assert.True(t, result.Available)
	require.NotNil(t, result.DistanceMeters)
	assert.Equal(t, 0.0, *result.DistanceMeters)
	assert.Nil(t, result.Geometry)
}
