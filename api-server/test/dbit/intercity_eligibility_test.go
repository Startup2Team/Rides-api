//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/intercity"
	"github.com/workspace/ride-platform/internal/packages"
)

// A moto can seat one passenger, so `total_seats <= capacity` was satisfied by
// a 1-seat "intercity trip" — and a passenger could then buy a two-hour
// Kigali -> Musanze journey on the back of a motorcycle. The seat floor is what
// stops that, and it is enforced at publish, server-side.
func TestPublishTrip_RejectsVehiclesBelowTheIntercitySeatFloor(t *testing.T) {
	ctx := context.Background()
	svc := newIntercityService()

	for _, tc := range []struct {
		typeCode string
		seats    int
	}{
		{"MOTO_BIKE", 1},
		{"TUK_TUK", 3},
		{"HEAVY_FUSO", 1}, // cargo, and one seat
	} {
		t.Run(tc.typeCode, func(t *testing.T) {
			userID, vehicleID := newIntercityDriverWithVehicle(t, ctx, tc.typeCode, tc.seats)

			_, err := svc.PublishTrip(ctx, userID, publishInput(t, ctx, vehicleID, 1))

			require.ErrorIs(t, err, intercity.ErrVehicleNotIntercityEligible,
				"%s must not be able to publish an intercity trip at any seat count", tc.typeCode)
		})
	}
}

// The mirror image: the rule must not lock out the vehicles intercity exists
// for. A cab is the smallest eligible vehicle — the boundary case of the floor.
func TestPublishTrip_AcceptsVehiclesAtAndAboveTheSeatFloor(t *testing.T) {
	ctx := context.Background()
	svc := newIntercityService()

	for _, tc := range []struct {
		typeCode string
		seats    int
	}{
		{"CAB_TAXI", 4}, // exactly MinIntercityVehicleSeats
		{"COASTER", 18},
	} {
		t.Run(tc.typeCode, func(t *testing.T) {
			userID, vehicleID := newIntercityDriverWithVehicle(t, ctx, tc.typeCode, tc.seats)

			trip, err := svc.PublishTrip(ctx, userID, publishInput(t, ctx, vehicleID, tc.seats))

			require.NoError(t, err)
			require.Equal(t, tc.seats, trip.TotalSeats)
			retireTrip(t, trip.ID)
		})
	}
}

// Seats declared per vehicle are NULL for everyone who registered before the
// column was collected. The fallback is the vehicle TYPE's capacity, never the
// table's structural bound of 30 — otherwise a cab with no declared seats could
// publish thirty of them.
func TestPublishTrip_UndeclaredSeatsFallBackToTheVehicleType(t *testing.T) {
	ctx := context.Background()
	svc := newIntercityService()

	userID, vehicleID := newIntercityDriverWithVehicle(t, ctx, "CAB_TAXI", 0) // passenger_seats NULL

	_, err := svc.PublishTrip(ctx, userID, publishInput(t, ctx, vehicleID, 30))
	require.Error(t, err, "a cab may not sell 30 seats just because it declared none")

	trip, err := svc.PublishTrip(ctx, userID, publishInput(t, ctx, vehicleID, 4))
	require.NoError(t, err, "but it may sell the four its type seats")
	retireTrip(t, trip.ID)
}

// --- fixtures ---------------------------------------------------------------

func newIntercityService() *intercity.Service {
	return intercity.NewService(
		intercity.NewRepository(pool),
		packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop()),
		zerolog.Nop(),
	)
}

// newIntercityDriverWithVehicle creates an APPROVED driver on `typeCode` with
// an active vehicle and enough credits to clear the publish credit gate.
// seats <= 0 leaves driver_vehicles.passenger_seats NULL.
func newIntercityDriverWithVehicle(t *testing.T, ctx context.Context, typeCode string, seats int) (userID, vehicleID string) {
	t.Helper()

	u, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-elig", "android", nil, nil, nil)
	require.NoError(t, err)
	profileID := newDriverProfile(t, ctx, u.ID, typeCode)

	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = $1`, typeCode).Scan(&vehicleTypeID))

	var declared *int
	if seats > 0 {
		declared = &seats
	}
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_vehicles (driver_id, vehicle_type_id, plate_number, passenger_seats,
		                             is_active, approval_status)
		VALUES ($1, $2, $3, $4, TRUE, 'APPROVED') RETURNING id`,
		profileID, vehicleTypeID, uniquePlate(), declared).Scan(&vehicleID))

	grantCredits(t, ctx, profileID, vehicleTypeID, 50, 0)
	return u.ID, vehicleID
}

var departCounter int

// publishInput is a valid publish payload — every field except the vehicle is
// deliberately fine, so a rejection can only be the seat floor. Each call takes
// a distinct departure minute: uq_intercity_trip_driver_slot is per (driver,
// depart_at) and would otherwise turn a second publish into a 409.
func publishInput(t *testing.T, ctx context.Context, vehicleID string, totalSeats int) intercity.PublishTripInput {
	t.Helper()
	departCounter++
	return intercity.PublishTripInput{
		Corridor:        intercityCorridor(t, ctx),
		VehicleID:       vehicleID,
		StagingAddress:  "Nyabugogo Taxi Park",
		DepartAt:        time.Now().Add(2*time.Hour + time.Duration(departCounter)*time.Minute),
		TotalSeats:      totalSeats,
		PricePerSeatRWF: 3000,
	}
}

// retireTrip keeps the shared, persistent test database clean: an OPEN trip
// left behind counts against maxOpenTripsPerDriver and is picked up by the
// global departure worker in later runs.
func retireTrip(t *testing.T, tripID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx,
			`UPDATE intercity_trips SET status='CANCELLED', cancelled_at=NOW(),
			        cancel_reason='test fixture', deleted_at=NOW() WHERE id=$1`, tripID)
		_, _ = pool.Exec(ctx,
			`UPDATE intercity_credit_charges SET status='CHARGED', charged_at=NOW()
			  WHERE trip_id=$1 AND status='PENDING'`, tripID)
	})
}
