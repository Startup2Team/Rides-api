//go:build integration

package dbit

import (
	"context"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/packages"
)

// CHARACTERISATION TESTS — these pin the EXISTING behaviour of deductOne before
// it is refactored, and must keep passing afterwards unchanged. City rides
// depend on every one of these properties.

// Paid credits are spent before bonus. This ordering is load-bearing: it is why
// deduct and refund are asymmetric, and why they must never be collapsed into a
// generic adjust(+/-1).
func TestLedger_DeductSpendsPaidBeforeBonus(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, userID := newLedgerFixture(t, ctx, "MOTO_BIKE", 1, 5)
	cust := insertIntercityCustomer(t, ctx)

	ok, err := svc.DeductForRide(ctx, userID, "MOTO_BIKE", newRideID(t, ctx, cust))
	require.NoError(t, err)
	require.True(t, ok)

	rides, bonus := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 0, rides, "the paid credit must go first")
	require.Equal(t, 5, bonus, "bonus must be untouched while paid credits remain")

	ok, err = svc.DeductForRide(ctx, userID, "MOTO_BIKE", newRideID(t, ctx, cust))
	require.NoError(t, err)
	require.True(t, ok)
	rides, bonus = entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 0, rides)
	require.Equal(t, 4, bonus, "bonus is spent only once paid is exhausted")
}

// (false, nil) means ALREADY CHARGED, not failed. Any caller that treats the
// bool as "did work happen" and retries on false will double-charge or, worse,
// penalise a driver who has already paid.
func TestLedger_DeductIsIdempotentOnKey(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, userID := newLedgerFixture(t, ctx, "MOTO_BIKE", 3, 0)
	cust := insertIntercityCustomer(t, ctx)
	key := newRideID(t, ctx, cust)

	ok, err := svc.DeductForRide(ctx, userID, "MOTO_BIKE", key)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = svc.DeductForRide(ctx, userID, "MOTO_BIKE", key)
	require.NoError(t, err, "a repeat must not error")
	require.False(t, ok, "(false, nil) = already charged")

	rides, _ := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 2, rides, "exactly one credit spent across both calls")
}

// An empty balance must refuse, not go negative: HasCredits gates going online,
// so a negative balance would lock the driver out of city rides entirely.
func TestLedger_DeductRefusesWhenEmpty(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, userID := newLedgerFixture(t, ctx, "MOTO_BIKE", 0, 0)
	cust := insertIntercityCustomer(t, ctx)

	_, err := svc.DeductForRide(ctx, userID, "MOTO_BIKE", newRideID(t, ctx, cust))
	require.ErrorIs(t, err, packages.ErrNoCredits)

	rides, bonus := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 0, rides)
	require.Equal(t, 0, bonus, "balance must never go negative")
}

// The ledger row records the balance AFTER the spend, which is what makes the
// ledger auditable and reconcilable against driver_entitlements.
func TestLedger_DeductWritesAuditableRow(t *testing.T) {
	ctx := context.Background()
	svc, profileID, _, userID := newLedgerFixture(t, ctx, "MOTO_BIKE", 2, 1)
	cust := insertIntercityCustomer(t, ctx)
	key := newRideID(t, ctx, cust)

	_, err := svc.DeductForRide(ctx, userID, "MOTO_BIKE", key)
	require.NoError(t, err)

	var entryType string
	var ridesDelta, balRides, balBonus int
	var sourceRide *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT entry_type, rides_delta, balance_rides, balance_bonus, source_ride_id
		  FROM ride_credit_ledger WHERE idempotency_key = $1`, key).
		Scan(&entryType, &ridesDelta, &balRides, &balBonus, &sourceRide))
	require.Equal(t, "RIDE_DEDUCTION", entryType)
	require.Equal(t, -1, ridesDelta)
	require.Equal(t, 1, balRides, "post-spend balance snapshot")
	require.Equal(t, 1, balBonus)
	require.NotNil(t, sourceRide, "city deductions carry source_ride_id")
	_ = profileID
}

// --- fixture --------------------------------------------------------------

func newLedgerFixture(t *testing.T, ctx context.Context, vehicleCode string, rides, bonus int) (*packages.LedgerService, string, string, string) {
	t.Helper()
	u, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-ledger", "android", nil, nil, nil)
	require.NoError(t, err)
	profileID := newDriverProfile(t, ctx, u.ID, vehicleCode)

	var vtID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = $1`, vehicleCode).Scan(&vtID))

	_, err = pool.Exec(ctx, `
		INSERT INTO driver_entitlements (driver_id, vehicle_type_id, rides_remaining, bonus_remaining)
		VALUES ($1, $2, $3, $4)`, profileID, vtID, rides, bonus)
	require.NoError(t, err)

	repo := packages.NewRepository(pool)
	return packages.NewLedgerService(repo, zerolog.Nop()), profileID, vtID, u.ID
}

// newRideID creates a minimal rides row. deductOne writes its key into BOTH
// ride_credit_ledger.idempotency_key (text) and source_ride_id (uuid FK to
// rides), so a city deduction key must be a real ride id — which is exactly why
// intercity needs its own source column and its own key format.
func newRideID(t *testing.T, ctx context.Context, customerID string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO rides (customer_id, transport_type, status,
		                   pickup_point, pickup_address, destination_point, destination_address)
		VALUES ($1, 'MOTO_BIKE', 'SEARCHING',
		        ST_SetSRID(ST_MakePoint(30.06, -1.94), 4326)::geography, 'Nyabugogo',
		        ST_SetSRID(ST_MakePoint(30.10, -1.95), 4326)::geography, 'Kimironko')
		RETURNING id`, customerID).Scan(&id))
	return id
}

func entitlement(t *testing.T, ctx context.Context, profileID, vtID string) (int, int) {
	t.Helper()
	var rides, bonus int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		 WHERE driver_id = $1 AND vehicle_type_id = $2`, profileID, vtID).Scan(&rides, &bonus))
	return rides, bonus
}

// --- intercity deduction ---------------------------------------------------

// The intercity sibling must write to the trip source column, never source_ride_id
// (which FKs to rides and could not hold a trip id anyway).
func TestLedger_DeductForIntercityWritesTripSource(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, _ := newLedgerFixture(t, ctx, "COASTER", 5, 0)
	tripID := insertIntercityTrip(t, ctx, 18)
	key := "intercity:" + tripID + ":1"

	ok, err := svc.DeductForIntercity(ctx, profileID, vtID, key, tripID)
	require.NoError(t, err)
	require.True(t, ok)

	var entryType string
	var sourceRide, sourceTrip *string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT entry_type, source_ride_id, source_intercity_trip_id
		  FROM ride_credit_ledger WHERE idempotency_key = $1`, key).
		Scan(&entryType, &sourceRide, &sourceTrip))
	require.Equal(t, "INTERCITY_DEDUCTION", entryType)
	require.Nil(t, sourceRide, "must NOT borrow the city ride column")
	require.NotNil(t, sourceTrip)
	require.Equal(t, tripID, *sourceTrip, "the charge is traceable to its trip")

	rides, _ := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 4, rides, "exactly one credit per seat")
}

// A retry after a crash must converge, not double-charge. (false, nil) means
// ALREADY CHARGED — a worker treating it as failure would bill a driver and
// then push them into arrears for non-payment.
func TestLedger_DeductForIntercityIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, _ := newLedgerFixture(t, ctx, "COASTER", 5, 0)
	tripID := insertIntercityTrip(t, ctx, 18)
	key := "intercity:" + tripID + ":1"

	ok, err := svc.DeductForIntercity(ctx, profileID, vtID, key, tripID)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = svc.DeductForIntercity(ctx, profileID, vtID, key, tripID)
	require.NoError(t, err)
	require.False(t, ok, "(false, nil) = already charged")

	rides, _ := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 4, rides, "one credit across both calls")
}

// One credit PER SEAT. v1 charged per booking (1 credit for a 6-seat booking,
// an 83% loss) and v2 reintroduced it by counting rows; this pins the fix.
func TestLedger_IntercityChargesOneCreditPerSeat(t *testing.T) {
	ctx := context.Background()
	svc, profileID, vtID, _ := newLedgerFixture(t, ctx, "COASTER", 10, 0)
	tripID := insertIntercityTrip(t, ctx, 18)

	for seq := 1; seq <= 6; seq++ {
		ok, err := svc.DeductForIntercity(ctx, profileID, vtID,
			fmt.Sprintf("intercity:%s:%d", tripID, seq), tripID)
		require.NoError(t, err)
		require.True(t, ok)
	}
	rides, _ := entitlement(t, ctx, profileID, vtID)
	require.Equal(t, 4, rides, "6 seats == 6 credits")
}

// A ledger row must name exactly one source.
func TestLedger_OneSourceOnlyIsEnforced(t *testing.T) {
	ctx := context.Background()
	_, profileID, vtID, _ := newLedgerFixture(t, ctx, "COASTER", 1, 0)
	tripID := insertIntercityTrip(t, ctx, 18)
	cust := insertIntercityCustomer(t, ctx)
	rideID := newRideID(t, ctx, cust)

	_, err := pool.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta,
		     balance_rides, balance_bonus, source_ride_id, source_intercity_trip_id, idempotency_key)
		VALUES ($1,$2,'INTERCITY_DEDUCTION',-1,0,0,0,$3,$4,$5)`,
		profileID, vtID, rideID, tripID, uniqueKey("both"))
	require.Error(t, err, "a row naming both a ride and a trip must be rejected")
}
