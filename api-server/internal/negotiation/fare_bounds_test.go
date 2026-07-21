package negotiation

import (
	"testing"

	"github.com/workspace/ride-platform/internal/ride"
)

func TestCheckVehicleFareBounds(t *testing.T) {
	cases := []struct {
		name          string
		transportType string
		amount        float64
		wantErr       bool
	}{
		{"moto at floor ok", "MOTO_BIKE", 500, false},
		{"moto at cap ok", "MOTO_BIKE", 4000, false},
		{"moto below floor rejected", "MOTO_BIKE", 499, true},
		{"moto above cap rejected", "MOTO_BIKE", 4001, true},
		{"tuktuk mid ok", "TUK_TUK", 3000, false},
		{"tuktuk above cap rejected", "TUK_TUK", 5001, true},
		{"cab mid ok", "CAB_TAXI", 8000, false},
		{"cab below floor rejected", "CAB_TAXI", 1999, true},
		{"hilux mid ok", "LIGHT_HILUX", 20000, false},
		{"hilux above cap rejected", "LIGHT_HILUX", 25001, true},
		{"fuso at floor ok", "HEAVY_FUSO", 10000, false},
		{"fuso below floor rejected", "HEAVY_FUSO", 9999, true},
		{"unknown type permissive ok", "SPACE_ROCKET", 50000, false},
		{"unknown type above permissive cap rejected", "SPACE_ROCKET", 100001, true},
		{"unknown type below permissive floor rejected", "SPACE_ROCKET", 499, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &ride.Ride{TransportType: tc.transportType}
			err := checkVehicleFareBounds(r, tc.amount)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %s amount %.0f, got nil", tc.transportType, tc.amount)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for %s amount %.0f, got %v", tc.transportType, tc.amount, err)
			}
		})
	}
}
