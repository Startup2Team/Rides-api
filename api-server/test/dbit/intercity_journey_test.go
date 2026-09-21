//go:build integration

package dbit

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/intercity"
)

// A whole realistic journey, end to end, against the real schema: a bus company
// publishes a Coaster on Kigali -> Musanze, real passengers book in sequence,
// the remaining-seat count they would see is asserted at every step, the trip
// sells out, and a late passenger is turned away.
//
// This exists to prove the pieces CONNECT — operator, driver, vehicle, trip,
// bookings and seat counters — not to re-test any one of them.
func TestJourney_CompanyCoasterSellsOut(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	const seats = 18

	// --- A bus company registers, with a driver on its roster ------------------
	operatorID, driverProfileID := newCompanyOperator(t, ctx, "Volcano Express")
	vehicleID := newVehicleFor(t, ctx, driverProfileID, "COASTER", seats)

	// --- It publishes tomorrow's 14:00 Kigali -> Musanze ----------------------
	tripID := publishTrip(t, ctx, operatorID, driverProfileID, vehicleID, seats, 3500)

	remaining := func() int {
		var total, held, booked int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT total_seats, held_seats, booked_seats FROM intercity_trips WHERE id=$1`,
			tripID).Scan(&total, &held, &booked))
		return total - held - booked
	}
	require.Equal(t, 18, remaining(), "a freshly published Coaster shows all 18 seats")

	// --- Passengers book. This is the requirement "show remaining seats". -----
	type step struct {
		who   string
		seats int
		left  int
	}
	for _, s := range []step{
		{"Jean books for himself and his wife", 2, 16},
		{"Claudine books for her family", 4, 12},
		{"Eric books alone", 1, 11},
		{"Mukamana books for three", 3, 8},
	} {
		cust := insertIntercityCustomer(t, ctx)
		b, err := repo.Hold(ctx, tripID, cust, s.seats, uniqueKey("j"), seats)
		require.NoError(t, err, s.who)
		_, err = repo.Confirm(ctx, b.ID, cust)
		require.NoError(t, err, s.who)
		require.Equal(t, s.left, remaining(), "after: %s", s.who)
	}

	// --- Someone cancels; the seats must come straight back to the pool -------
	cancelCust := insertIntercityCustomer(t, ctx)
	cancelled, err := repo.Hold(ctx, tripID, cancelCust, 5, uniqueKey("j"), seats)
	require.NoError(t, err)
	require.Equal(t, 3, remaining(), "a hold reserves seats immediately, before payment")
	require.NoError(t, repo.CancelBooking(ctx, cancelled.ID, cancelCust, "CUSTOMER"))
	require.Equal(t, 8, remaining(), "cancelling returns every seat to the pool")

	// --- The last 8 sell, and the vehicle is FULL -----------------------------
	last := insertIntercityCustomer(t, ctx)
	lastBooking, err := repo.Hold(ctx, tripID, last, 4, uniqueKey("j"), seats)
	require.NoError(t, err)
	_, err = repo.Confirm(ctx, lastBooking.ID, last)
	require.NoError(t, err)

	filler := insertIntercityCustomer(t, ctx)
	fb, err := repo.Hold(ctx, tripID, filler, 4, uniqueKey("j"), seats)
	require.NoError(t, err)
	_, err = repo.Confirm(ctx, fb.ID, filler)
	require.NoError(t, err)
	require.Equal(t, 0, remaining(), "18 seats sold: the trip is FULL")

	// --- A late passenger is turned away, not oversold ------------------------
	late := insertIntercityCustomer(t, ctx)
	_, err = repo.Hold(ctx, tripID, late, 1, uniqueKey("j"), seats)
	require.ErrorIs(t, err, intercity.ErrSeatsUnavailable,
		"a full Coaster must stop accepting bookings")

	// --- The manifest the driver sees at the park ----------------------------
	var passengers, soldSeats int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(seats),0) FROM intercity_bookings
		 WHERE trip_id=$1 AND deleted_at IS NULL AND status='CONFIRMED'`, tripID).
		Scan(&passengers, &soldSeats))
	require.Equal(t, 6, passengers, "six separate bookings share this vehicle")
	require.Equal(t, 18, soldSeats, "and they account for exactly every seat")

	// --- The trip is attributable to the COMPANY, driven by its driver --------
	var gotOperator, gotDriver, operatorName, kind string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT t.operator_id, t.driver_id, o.display_name, o.kind
		  FROM intercity_trips t JOIN intercity_operators o ON o.id = t.operator_id
		 WHERE t.id=$1`, tripID).Scan(&gotOperator, &gotDriver, &operatorName, &kind))
	require.Equal(t, operatorID, gotOperator)
	require.Equal(t, driverProfileID, gotDriver, "the ASSIGNED driver, not the owner")
	require.Equal(t, "Volcano Express", operatorName, "passengers see the company name")
	require.Equal(t, "COMPANY", kind)
}

// The same flow for an individual running their own Hiace: one operator, one
// vehicle, and they are their own driver. It must be the SAME code path.
func TestJourney_IndividualHiaceIsTheSameFlow(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	const seats = 8

	driverUser, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-solo", "android", nil, nil, nil)
	require.NoError(t, err)
	driverProfileID := newDriverProfile(t, ctx, driverUser.ID, "HIACE")

	// An individual is an operator whose owner IS the driver.
	var operatorID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_operators (owner_user_id, display_name, kind, approval_status)
		VALUES ($1, 'Kwizera Transport', 'INDIVIDUAL', 'APPROVED') RETURNING id`,
		driverUser.ID).Scan(&operatorID))
	_, err = pool.Exec(ctx, `
		INSERT INTO intercity_operator_drivers (operator_id, driver_profile_id)
		VALUES ($1, $2)`, operatorID, driverProfileID)
	require.NoError(t, err)

	vehicleID := newVehicleFor(t, ctx, driverProfileID, "HIACE", seats)
	tripID := publishTrip(t, ctx, operatorID, driverProfileID, vehicleID, seats, 3000)

	cust := insertIntercityCustomer(t, ctx)
	b, err := repo.Hold(ctx, tripID, cust, 2, uniqueKey("solo"), seats)
	require.NoError(t, err)
	_, err = repo.Confirm(ctx, b.ID, cust)
	require.NoError(t, err)

	var held, booked, owner string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT t.held_seats::text, t.booked_seats::text, o.owner_user_id::text
		  FROM intercity_trips t JOIN intercity_operators o ON o.id=t.operator_id
		 WHERE t.id=$1`, tripID).Scan(&held, &booked, &owner))
	require.Equal(t, "0", held)
	require.Equal(t, "2", booked)
	require.Equal(t, driverUser.ID, owner, "the individual owns the operator and drives it")
}

// --- helpers --------------------------------------------------------------

func newDriverProfile(t *testing.T, ctx context.Context, userID, transportType string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_profiles
		    (user_id, transport_type, vehicle_plate, license_number, date_of_birth,
		     city, momo_pay_code, approval_status)
		VALUES ($1, $2, $3, $4, '1990-01-01', 'Kigali', '+250788000000', 'APPROVED')
		RETURNING id`, userID, transportType, uniquePlate(), "DL-"+uniqueKey("j")).Scan(&id))
	return id
}

func newCompanyOperator(t *testing.T, ctx context.Context, name string) (string, string) {
	t.Helper()
	authRepo := auth.NewRepository(pool)

	ownerUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-owner", "android", nil, nil, nil)
	require.NoError(t, err)

	var operatorID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO intercity_operators (owner_user_id, display_name, kind, approval_status)
		VALUES ($1, $2, 'COMPANY', 'APPROVED') RETURNING id`, ownerUser.ID, name).Scan(&operatorID))

	// The owner does NOT drive. An employed driver does.
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-employed", "android", nil, nil, nil)
	require.NoError(t, err)
	driverProfileID := newDriverProfile(t, ctx, driverUser.ID, "COASTER")

	_, err = pool.Exec(ctx, `
		INSERT INTO intercity_operator_drivers (operator_id, driver_profile_id)
		VALUES ($1, $2)`, operatorID, driverProfileID)
	require.NoError(t, err)
	return operatorID, driverProfileID
}

func newVehicleFor(t *testing.T, ctx context.Context, driverProfileID, typeCode string, seats int) string {
	t.Helper()
	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = $1`, typeCode).Scan(&vehicleTypeID))

	var vehicleID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_vehicles (driver_id, vehicle_type_id, plate_number, passenger_seats,
		                             is_active, approval_status)
		VALUES ($1, $2, $3, $4, TRUE, 'APPROVED') RETURNING id`,
		driverProfileID, vehicleTypeID, uniquePlate(), seats).Scan(&vehicleID))
	return vehicleID
}

func publishTrip(t *testing.T, ctx context.Context, operatorID, driverProfileID, vehicleID string, seats, price int) string {
	t.Helper()
	corridor := intercityCorridor(t, ctx)
	var tripID string
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO intercity_trips (
			operator_id, driver_id, vehicle_id, corridor, origin_name, destination_name,
			origin_point, destination_point, staging_address, depart_at,
			total_seats, price_per_seat_rwf)
		VALUES ($1, $2, $3, $4, 'Kigali', 'Musanze',
			ST_SetSRID(ST_MakePoint(30.0619, -1.9441), 4326)::geography,
			ST_SetSRID(ST_MakePoint(29.6344, -1.4995), 4326)::geography,
			'Nyabugogo Taxi Park', NOW() + interval '%d hours', $5, $6)
		RETURNING id`, 20), operatorID, driverProfileID, vehicleID, corridor, seats, price).Scan(&tripID))

	// Same reason as insertIntercityTrip: the sweeper is global and this
	// database persists, so a fixture must not leave live bookings or non-zero
	// counters behind for later runs to trip over.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx,
			`UPDATE intercity_bookings SET status='EXPIRED', updated_at=NOW()
			  WHERE trip_id=$1 AND status IN ('HELD','CONFIRMED')`, tripID)
		_, _ = pool.Exec(ctx,
			`UPDATE intercity_trips SET held_seats=0, booked_seats=0 WHERE id=$1`, tripID)
	})

	return tripID
}
