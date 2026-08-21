package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── updateLocationRequest / BatchLocationUpdate — validator tags ──────────
//
// go-playground/validator's `required` tag rejects the zero value, not just
// out-of-range values. Latitude 0 is the equator, which runs through Uganda
// (the expansion market); longitude 0 is the prime meridian. A driver
// standing exactly on either must not be rejected as invalid input.

func TestUpdateLocationRequest_Validation(t *testing.T) {
	cases := []struct {
		name    string
		body    updateLocationRequest
		wantErr bool
	}{
		{"equator latitude 0.0 is valid", updateLocationRequest{Lat: 0, Lng: 30.06}, false},
		{"prime meridian longitude 0.0 is valid", updateLocationRequest{Lat: -1.94, Lng: 0}, false},
		{"both lat and lng 0.0 is valid (null island is a real coordinate)", updateLocationRequest{Lat: 0, Lng: 0}, false},
		{"ordinary Kigali coordinate is valid", updateLocationRequest{Lat: -1.9441, Lng: 30.0619}, false},
		{"lat above 90 is rejected", updateLocationRequest{Lat: 90.1, Lng: 30}, true},
		{"lat below -90 is rejected", updateLocationRequest{Lat: -90.1, Lng: 30}, true},
		{"lng above 180 is rejected", updateLocationRequest{Lat: 0, Lng: 180.1}, true},
		{"lng below -180 is rejected", updateLocationRequest{Lat: 0, Lng: -180.1}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Struct(tc.body)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBatchLocationUpdate_Validation(t *testing.T) {
	cases := []struct {
		name    string
		body    BatchLocationUpdate
		wantErr bool
	}{
		{"equator latitude 0.0 is valid", BatchLocationUpdate{Lat: 0, Lng: 30.06}, false},
		{"prime meridian longitude 0.0 is valid", BatchLocationUpdate{Lat: -1.94, Lng: 0}, false},
		{"both lat and lng 0.0 is valid (null island is a real coordinate)", BatchLocationUpdate{Lat: 0, Lng: 0}, false},
		{"ordinary Kigali coordinate is valid", BatchLocationUpdate{Lat: -1.9441, Lng: 30.0619}, false},
		{"lat above 90 is rejected", BatchLocationUpdate{Lat: 90.1, Lng: 30}, true},
		{"lat below -90 is rejected", BatchLocationUpdate{Lat: -90.1, Lng: 30}, true},
		{"lng above 180 is rejected", BatchLocationUpdate{Lat: 0, Lng: 180.1}, true},
		{"lng below -180 is rejected", BatchLocationUpdate{Lat: 0, Lng: -180.1}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Struct(tc.body)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
