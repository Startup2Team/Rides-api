package ride

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── updateCustomerLocationRequest — validator tags ─────────────────────────
//
// Code-review #4 / security F5: `validate:"required,..."` on Lat/Lng makes
// go-playground/validator reject the zero value, not just out-of-range
// values. Latitude 0 is the equator, which runs through Uganda (the
// expansion market) — a customer standing exactly on it, or a client that
// sends 0.0 for any other reason, must not be rejected as invalid input.

func TestUpdateCustomerLocationRequest_Validation(t *testing.T) {
	speed := 40.0
	heading := 180.0

	cases := []struct {
		name    string
		body    updateCustomerLocationRequest
		wantErr bool
	}{
		{"equator latitude 0.0 is valid", updateCustomerLocationRequest{Lat: 0, Lng: 30.06}, false},
		{"prime meridian longitude 0.0 is valid", updateCustomerLocationRequest{Lat: -1.94, Lng: 0}, false},
		{"both lat and lng 0.0 is valid (null island is a real coordinate)", updateCustomerLocationRequest{Lat: 0, Lng: 0}, false},
		{"ordinary Kigali coordinate is valid", updateCustomerLocationRequest{Lat: -1.9441, Lng: 30.0619}, false},
		{"lat above 90 is rejected", updateCustomerLocationRequest{Lat: 90.1, Lng: 30}, true},
		{"lat below -90 is rejected", updateCustomerLocationRequest{Lat: -90.1, Lng: 30}, true},
		{"lng above 180 is rejected", updateCustomerLocationRequest{Lat: 0, Lng: 180.1}, true},
		{"lng below -180 is rejected", updateCustomerLocationRequest{Lat: 0, Lng: -180.1}, true},
		{"nil speed/heading are valid (optional fields)", updateCustomerLocationRequest{Lat: 0, Lng: 0}, false},
		{"in-range speed and heading are valid", updateCustomerLocationRequest{Lat: 0, Lng: 0, SpeedKMH: &speed, Heading: &heading}, false},
		{"negative speed is rejected", updateCustomerLocationRequest{Lat: 0, Lng: 0, SpeedKMH: ptr(-1.0)}, true},
		{"implausible speed is rejected", updateCustomerLocationRequest{Lat: 0, Lng: 0, SpeedKMH: ptr(301.0)}, true},
		{"heading below 0 is rejected", updateCustomerLocationRequest{Lat: 0, Lng: 0, Heading: ptr(-0.1)}, true},
		{"heading above 360 is rejected", updateCustomerLocationRequest{Lat: 0, Lng: 0, Heading: ptr(360.1)}, true},
		{"heading exactly 360 is valid (full compass, boundary inclusive)", updateCustomerLocationRequest{Lat: 0, Lng: 0, Heading: ptr(360.0)}, false},
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

func ptr(f float64) *float64 { return &f }
