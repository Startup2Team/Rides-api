//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/intercity"
	"github.com/workspace/ride-platform/internal/packages"
)

// Publishing must work from the corridor ALONE. A corridor is a fixed pair of
// places, so requiring the client to send origin_name, destination_name and
// four coordinates meant every caller had to invent geography the server
// already knows — and it made publish impossible from the app, because
// intercity_corridors had no coordinates while the trip's points are NOT NULL.
//
// Two drivers typing "Musanze" would also pin it in two different places: the
// exact fragmentation the corridor lookup table exists to prevent.
func TestPublish_DerivesPlacesAndPointsFromTheCorridor(t *testing.T) {
	ctx := context.Background()
	svc := intercity.NewService(intercity.NewRepository(pool),
		packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop()), zerolog.Nop())

	driverUserID, _, vehicleID := eligibleDriverWithCredits(t, ctx, "COASTER", 18)
	corridor := intercityCorridor(t, ctx)

	trip, err := svc.PublishTrip(ctx, driverUserID, intercity.PublishTripInput{
		Corridor:        corridor,
		VehicleID:       vehicleID,
		DepartAt:        time.Now().Add(6 * time.Hour),
		TotalSeats:      18,
		PricePerSeatRWF: 3500,
		StagingAddress:  "Nyabugogo Taxi Park",
	})
	require.NoError(t, err, "publish must succeed from the corridor alone")

	require.Equal(t, "Kigali", trip.OriginName, "origin name comes from the corridor")
	require.Equal(t, "Musanze", trip.DestinationName)

	// The geography must be the corridor's, not NULL and not invented.
	var originTxt, destTxt string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT ST_AsText(origin_point::geometry), ST_AsText(destination_point::geometry)
		  FROM intercity_trips WHERE id = $1`, trip.ID).Scan(&originTxt, &destTxt))
	require.Equal(t, "POINT(30.0588 -1.9302)", originTxt)
	require.Equal(t, "POINT(29.6344 -1.4995)", destTxt)
}

// A motorcycle cannot carry passengers between cities. The seat floor must bite
// before anything else, and say something a driver can act on.
func TestPublish_RejectsAVehicleThatCannotRunIntercity(t *testing.T) {
	ctx := context.Background()
	svc := intercity.NewService(intercity.NewRepository(pool),
		packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop()), zerolog.Nop())

	driverUserID, _, vehicleID := eligibleDriverWithCredits(t, ctx, "MOTO_BIKE", 1)
	corridor := intercityCorridor(t, ctx)

	_, err := svc.PublishTrip(ctx, driverUserID, intercity.PublishTripInput{
		Corridor:        corridor,
		VehicleID:       vehicleID,
		DepartAt:        time.Now().Add(6 * time.Hour),
		TotalSeats:      1,
		PricePerSeatRWF: 3000,
		StagingAddress:  "Nyabugogo Taxi Park",
	})
	require.Error(t, err, "a moto must never be publishable for intercity")
}

func eligibleDriverWithCredits(t *testing.T, ctx context.Context, typeCode string, seats int) (string, string, string) {
	t.Helper()
	driverUser := insertIntercityCustomer(t, ctx)
	profileID := newDriverProfile(t, ctx, driverUser, typeCode)
	vehicleID := newVehicleFor(t, ctx, profileID, typeCode, seats)

	var vtID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = $1`, typeCode).Scan(&vtID))
	grantCredits(t, ctx, profileID, vtID, 50, 0)

	var operatorID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_operators (owner_user_id, display_name, kind, approval_status)
		VALUES ($1, 'Pub Test', 'INDIVIDUAL', 'APPROVED')
		ON CONFLICT DO NOTHING RETURNING id`, driverUser).Scan(&operatorID))
	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_operator_drivers (operator_id, driver_profile_id)
		VALUES ($1,$2) ON CONFLICT DO NOTHING`, operatorID, profileID)
	require.NoError(t, err)

	return driverUser, profileID, vehicleID
}
