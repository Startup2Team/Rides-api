package driver

import (
	"net/url"
	"testing"
)

// parseDemandHeatmapParams holds all the input-sanitising logic for the demand
// heatmap: defaults, clamping, and the "lat+lng must come as a valid pair" rule.
// These are exactly the branches a malformed client request would hit, so we
// pin every bound here — a regression in clamping would otherwise silently ship
// (e.g. a 0-minute window returning nothing, or an unbounded radius scanning the
// whole table).
func vals(pairs map[string]string) url.Values {
	v := url.Values{}
	for k, val := range pairs {
		v.Set(k, val)
	}
	return v
}

func TestParseDemandHeatmapParams_Defaults(t *testing.T) {
	// No params → documented defaults, no scoping.
	win, radM, center, err := parseDemandHeatmapParams(url.Values{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if win != 120 {
		t.Errorf("windowMin default = %d, want 120", win)
	}
	if radM != 5000 {
		t.Errorf("radiusM default = %d, want 5000", radM)
	}
	if center != nil {
		t.Errorf("center = %+v, want nil when no lat/lng given", center)
	}
}

func TestParseDemandHeatmapParams_Clamping(t *testing.T) {
	tests := []struct {
		name       string
		in         map[string]string
		wantWindow int
		wantRadius int
	}{
		{"window below floor clamps up", map[string]string{"window_min": "1"}, 15, 5000},
		{"window above ceiling clamps down", map[string]string{"window_min": "99999"}, 1440, 5000},
		{"window zero falls back to default", map[string]string{"window_min": "0"}, 120, 5000},
		{"window garbage falls back to default", map[string]string{"window_min": "abc"}, 120, 5000},
		{"radius below floor clamps up", map[string]string{"radius_km": "0.1"}, 120, 500},
		{"radius above ceiling clamps down", map[string]string{"radius_km": "500"}, 120, 50000},
		{"radius normal converts km→m", map[string]string{"radius_km": "8"}, 120, 8000},
		{"both provided in range", map[string]string{"window_min": "60", "radius_km": "3"}, 60, 3000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			win, radM, _, err := parseDemandHeatmapParams(vals(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if win != tc.wantWindow {
				t.Errorf("windowMin = %d, want %d", win, tc.wantWindow)
			}
			if radM != tc.wantRadius {
				t.Errorf("radiusM = %d, want %d", radM, tc.wantRadius)
			}
		})
	}
}

func TestParseDemandHeatmapParams_Coordinates(t *testing.T) {
	// Valid pair → scoped query with the exact coordinates.
	_, _, center, err := parseDemandHeatmapParams(vals(map[string]string{"lat": "-1.95", "lng": "30.06"}))
	if err != nil {
		t.Fatalf("unexpected error for valid coords: %v", err)
	}
	if center == nil || center.Lat != -1.95 || center.Lng != 30.06 {
		t.Fatalf("center = %+v, want {-1.95, 30.06}", center)
	}
}

func TestParseDemandHeatmapParams_BadCoordinates(t *testing.T) {
	// A lone or out-of-range coordinate must ERROR rather than silently drop the
	// scope — otherwise a driver asking for "near me" would get platform-wide data.
	bad := []map[string]string{
		{"lat": "-1.95"},                // lng missing
		{"lng": "30.06"},                // lat missing
		{"lat": "abc", "lng": "30.06"},  // lat not a number
		{"lat": "-1.95", "lng": "xyz"},  // lng not a number
		{"lat": "-100", "lng": "30.06"}, // lat out of range
		{"lat": "-1.95", "lng": "200"},  // lng out of range
	}
	for _, in := range bad {
		if _, _, center, err := parseDemandHeatmapParams(vals(in)); err == nil {
			t.Errorf("input %v: expected error, got center=%+v", in, center)
		}
	}
}
