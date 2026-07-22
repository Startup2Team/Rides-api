package negotiation

import (
	"fmt"

	"github.com/workspace/ride-platform/internal/ride"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// vehicleFareBound is the hard floor/cap (RWF) a negotiated fare may take for a
// given vehicle type. These are absolute guardrails against exploitation on the
// negotiation path — neither driver nor customer may lock a fare outside them —
// and are independent of the estimate-anchored band in fareBounds. In Kigali a
// moto ride never realistically exceeds ~4000 RWF nor drops below 500 RWF;
// larger vehicles carry proportionally higher floors and caps.
type vehicleFareBound struct {
	floor float64
	cap   float64
}

// vehicleFareBounds is keyed by ride.TransportType (vehicle_type_code). An
// unknown type falls back to permissiveFareBound so a newly added vehicle type
// is never blocked outright — it still gets a sane absolute ceiling.
var vehicleFareBounds = map[string]vehicleFareBound{
	"MOTO_BIKE":   {floor: 500, cap: 4000},
	"TUK_TUK":     {floor: 700, cap: 5000},
	"CAB_TAXI":    {floor: 2000, cap: 15000},
	"LIGHT_HILUX": {floor: 3000, cap: 25000},
	"HEAVY_FUSO":  {floor: 10000, cap: 80000},
}

// permissiveFareBound applies to any vehicle type not listed above.
var permissiveFareBound = vehicleFareBound{floor: 500, cap: 100000}

// boundsForVehicle returns the hard fare bounds for a ride's vehicle type,
// falling back to the permissive bound for unknown types.
func boundsForVehicle(transportType string) vehicleFareBound {
	if b, ok := vehicleFareBounds[transportType]; ok {
		return b
	}
	return permissiveFareBound
}

// checkVehicleFareBounds rejects a negotiated amount that falls outside the hard
// per-vehicle floor/cap. It is unconditional (no config lookup, cannot be
// disabled) so every fare-setting path fails closed on an exploitative amount.
// Returns a 400 VALIDATION AppError the HTTP handler surfaces to the mobile app.
func checkVehicleFareBounds(r *ride.Ride, amount float64) error {
	b := boundsForVehicle(r.TransportType)
	if amount < b.floor || amount > b.cap {
		return apperrors.New(400, "VALIDATION", fmt.Sprintf(
			"Fare must be between %d and %d RWF for %s",
			int(b.floor), int(b.cap), r.TransportType,
		))
	}
	return nil
}
