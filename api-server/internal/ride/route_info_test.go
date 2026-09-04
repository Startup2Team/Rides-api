package ride

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── realRouteInfo — the route_* display-field gate ──────────────────────────
//
// GetRouteDetails' `found` return is always true (it falls back to a
// Haversine estimate rather than reporting "not found"), so CreateRide must
// NOT build the response's route_* fields off found alone — that would label
// a straight-line estimate as a real-road route. realRouteInfo is the exact
// gate CreateRide uses; testing it here proves the informational route_*
// fields on POST /customer/rides are absent on OSRM-off/timeout/Haversine
// fallback and present (with geometry, when OSRM returned one) on a genuine
// OSRM route — without spinning up the full Service's DB/Redis dependencies.

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

func TestRealRouteInfo(t *testing.T) {
	cases := []struct {
		name            string
		distanceKM      float64
		durationMinutes int
		durationSeconds *int
		geometry        *string
		wantNil         bool
	}{
		{
			name:            "OSRM disabled or lookup failed — Haversine fallback with no geometry",
			distanceKM:      9.125, // straight-line * 1.25 road factor
			durationMinutes: 21,
			durationSeconds: nil,
			geometry:        nil,
			wantNil:         true,
		},
		{
			name:            "OSRM timed out / returned NoRoute — Haversine fallback",
			distanceKM:      3.4,
			durationMinutes: 10,
			durationSeconds: nil,
			geometry:        nil,
			wantNil:         true,
		},
		{
			name:            "real OSRM route with geometry — route_* fields present",
			distanceKM:      7.32,
			durationMinutes: 12,
			durationSeconds: intPtr(690),
			geometry:        strPtr("abc123polyline"),
			wantNil:         false,
		},
		{
			name:            "real OSRM route with no polyline (e.g. origin==destination) — still a real route",
			distanceKM:      0,
			durationMinutes: 1,
			durationSeconds: intPtr(0),
			geometry:        nil,
			wantNil:         false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := realRouteInfo(tc.distanceKM, tc.durationMinutes, tc.durationSeconds, tc.geometry)

			if tc.wantNil {
				assert.Nil(t, got, "Haversine-fallback results must never be surfaced as a real route")
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tc.distanceKM, got.DistanceKM)
			assert.Equal(t, tc.durationMinutes, got.DurationMinutes)
			require.NotNil(t, got.DurationSeconds)
			assert.Equal(t, *tc.durationSeconds, *got.DurationSeconds)
			assert.Equal(t, tc.geometry, got.Geometry)
		})
	}
}
