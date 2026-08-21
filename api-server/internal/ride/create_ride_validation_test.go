package ride

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── createRideRequest — validator tags ─────────────────────────────────────
//
// go-playground/validator's `required` tag rejects the zero value, not just
// out-of-range values. Latitude 0 is the equator, which runs through Uganda
// (the expansion market); longitude 0 is the prime meridian. A pickup or
// destination that lands exactly on either must not be rejected as invalid
// input.

func TestCreateRideRequest_Validation(t *testing.T) {
	base := createRideRequest{
		PickupAddr:    "Kigali Convention Centre",
		DestAddr:      "Kigali International Airport",
		TransportType: "MOTO_BIKE",
	}

	cases := []struct {
		name    string
		mutate  func(r createRideRequest) createRideRequest
		wantErr bool
	}{
		{
			name: "equator pickup latitude 0.0 is valid",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = 0, 30.06
				r.DestLat, r.DestLng = -1.94, 30.13
				return r
			},
			wantErr: false,
		},
		{
			name: "prime meridian pickup longitude 0.0 is valid",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -1.94, 0
				r.DestLat, r.DestLng = -1.94, 30.13
				return r
			},
			wantErr: false,
		},
		{
			name: "pickup at 0/0 (null island) is valid",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = 0, 0
				r.DestLat, r.DestLng = -1.94, 30.13
				return r
			},
			wantErr: false,
		},
		{
			name: "destination at 0/0 (null island) is valid",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -1.94, 30.06
				r.DestLat, r.DestLng = 0, 0
				return r
			},
			wantErr: false,
		},
		{
			name: "ordinary Kigali coordinates are valid",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -1.9441, 30.0619
				r.DestLat, r.DestLng = -1.9706, 30.1044
				return r
			},
			wantErr: false,
		},
		{
			name: "pickup lat above 90 is rejected",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = 90.1, 30
				r.DestLat, r.DestLng = -1.94, 30.13
				return r
			},
			wantErr: true,
		},
		{
			name: "pickup lat below -90 is rejected",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -90.1, 30
				r.DestLat, r.DestLng = -1.94, 30.13
				return r
			},
			wantErr: true,
		},
		{
			name: "dest lng above 180 is rejected",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -1.94, 30.06
				r.DestLat, r.DestLng = 0, 180.1
				return r
			},
			wantErr: true,
		},
		{
			name: "dest lng below -180 is rejected",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupLat, r.PickupLng = -1.94, 30.06
				r.DestLat, r.DestLng = 0, -180.1
				return r
			},
			wantErr: true,
		},
		{
			name: "missing pickup_address is still rejected (required untouched)",
			mutate: func(r createRideRequest) createRideRequest {
				r.PickupAddr = ""
				r.PickupLat, r.PickupLng = -1.94, 30.06
				r.DestLat, r.DestLng = -1.97, 30.10
				return r
			},
			wantErr: true,
		},
		{
			name: "missing transport_type is still rejected (required untouched)",
			mutate: func(r createRideRequest) createRideRequest {
				r.TransportType = ""
				r.PickupLat, r.PickupLng = -1.94, 30.06
				r.DestLat, r.DestLng = -1.97, 30.10
				return r
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Struct(tc.mutate(base))
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
