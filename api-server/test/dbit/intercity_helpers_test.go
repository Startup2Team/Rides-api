//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
)

// intercityCorridor ensures the Phase-1 corridor exists and returns its code.
// Corridors are a lookup table rather than free text: "Kigali", "kigali" and
// "Kigali " would otherwise be three different corridors, and a passenger
// searching a route that has trips would silently see an empty list.
func intercityCorridor(t *testing.T, ctx context.Context) string {
	t.Helper()
	const code = "KGL_MUS"
	_, err := pool.Exec(ctx, `
		INSERT INTO intercity_corridors (code, origin_name, destination_name)
		VALUES ($1, 'Kigali', 'Musanze') ON CONFLICT (code) DO NOTHING`, code)
	require.NoError(t, err)
	return code
}

// insertIntercityCustomer creates a customer user usable as a booking owner.
func insertIntercityCustomer(t *testing.T, ctx context.Context) string {
	t.Helper()
	u, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-ic", "android", nil, nil, nil)
	require.NoError(t, err)
	return u.ID
}

// insertIntercityTrip creates an OPEN trip departing in two hours on a
// LIGHT_HILUX, with the given capacity.
func insertIntercityTrip(t *testing.T, ctx context.Context, totalSeats int) string {
	t.Helper()
	corridor := intercityCorridor(t, ctx)

	driverUser, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-icd", "android", nil, nil, nil)
	require.NoError(t, err)

	var profileID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_profiles
		    (user_id, transport_type, vehicle_plate, license_number, date_of_birth,
		     city, momo_pay_code, approval_status)
		VALUES ($1, 'LIGHT_HILUX', $2, $3, '1995-01-01', 'Kigali', '+250788000000', 'APPROVED')
		RETURNING id`, driverUser.ID, uniquePlate(), "DL-"+uniqueKey("ic")).Scan(&profileID))

	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = 'LIGHT_HILUX'`).Scan(&vehicleTypeID))

	var vehicleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_vehicles (driver_id, vehicle_type_id, plate_number, is_active, approval_status)
		VALUES ($1, $2, $3, TRUE, 'APPROVED') RETURNING id`,
		profileID, vehicleTypeID, uniquePlate()).Scan(&vehicleID))

	var tripID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_trips (
			driver_id, vehicle_id, corridor, origin_name, destination_name,
			origin_point, destination_point, staging_address, depart_at,
			total_seats, price_per_seat_rwf)
		VALUES ($1, $2, $3, 'Kigali', 'Musanze',
			ST_SetSRID(ST_MakePoint(30.0619, -1.9441), 4326)::geography,
			ST_SetSRID(ST_MakePoint(29.6344, -1.4995), 4326)::geography,
			'Nyabugogo Taxi Park', NOW() + interval '2 hours', $4, 3000)
		RETURNING id`, profileID, vehicleID, corridor, totalSeats).Scan(&tripID))
	return tripID
}

// intercityTripDriver returns the driver profile id and vehicle type id behind a
// trip — the pair a credit charge must freeze at departure, because balances are
// held per (driver, vehicle_type).
func intercityTripDriver(t *testing.T, ctx context.Context, tripID string) (string, string) {
	t.Helper()
	var driverID, vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT t.driver_id, v.vehicle_type_id
		  FROM intercity_trips t JOIN driver_vehicles v ON v.id = t.vehicle_id
		 WHERE t.id = $1`, tripID).Scan(&driverID, &vehicleTypeID))
	return driverID, vehicleTypeID
}
