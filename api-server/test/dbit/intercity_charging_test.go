//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/intercity"
	"github.com/workspace/ride-platform/internal/packages"
)

// Obligations are charged per SEAT, not per booking. v1 charged per booking
// (one credit for a six-seat booking, an 83% loss) and v2 reintroduced the same
// bug one layer up by counting booking ROWS.
func TestObligations_ChargePerSeatNotPerBooking(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 18)

	// ONE booking of four seats.
	cust := insertIntercityCustomer(t, ctx)
	b, err := repo.Hold(ctx, tripID, cust, 4, uniqueKey("ob"), 18)
	require.NoError(t, err)
	_, err = repo.Confirm(ctx, b.ID, cust)
	require.NoError(t, err)

	n, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)
	require.Equal(t, 4, n, "4 seats => 4 charge rows, not 1")
}

// CONFIRMED and BOARDED are DISJOINT statuses. A partially-boarded manifest —
// the normal case, since marking is optional — must still charge every
// ever-confirmed seat.
func TestObligations_PartiallyBoardedChargesAllSeats(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 18)

	boarded := confirmSeats(t, ctx, repo, tripID, 4)
	confirmSeats(t, ctx, repo, tripID, 2) // driver never tapped Board for these
	_, err := pool.Exec(ctx,
		`UPDATE intercity_bookings SET status='BOARDED', boarded_at=NOW() WHERE id=$1`, boarded)
	require.NoError(t, err)

	n, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)
	require.Equal(t, 6, n, "4 boarded + 2 confirmed = 6 chargeable seats")
}

// Not pressing Start must NOT be a free trip. The obligation is created on the
// SERVER's clock by the departure worker, never by a driver action.
func TestObligations_CreatedWithoutStartBeingCalled(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)

	// RunDepartures is GLOBAL and oldest-first. Drain any historical backlog
	// BEFORE creating this test's trip — draining afterwards would consume it.
	for {
		n, _ := repo.RunDepartures(ctx, 500)
		if n == 0 {
			break
		}
	}

	tripID := insertIntercityTrip(t, ctx, 18)
	confirmSeats(t, ctx, repo, tripID, 3)

	// Departure time arrives; the driver never touches the app.
	_, err := pool.Exec(ctx,
		`UPDATE intercity_trips SET depart_at = NOW() - interval '1 minute' WHERE id=$1`, tripID)
	require.NoError(t, err)

	swept, err := repo.RunDepartures(ctx, 50)
	require.NoError(t, err)
	require.GreaterOrEqual(t, swept, 1)

	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM intercity_credit_charges WHERE trip_id=$1`, tripID).Scan(&rows))
	require.Equal(t, 3, rows, "the driver is billed whether or not they pressed Start")
}

// A driver must not be able to erase the bill by marking everyone a no-show.
// Marks beyond 40% of the vehicle are recorded but not deducted.
func TestObligations_NoShowBudgetCapsTheDiscount(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 10)

	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, confirmSeats(t, ctx, repo, tripID, 2))
	}
	// The driver marks EVERY passenger a no-show: 10 of 10 seats.
	for _, id := range ids {
		_, err := pool.Exec(ctx,
			`UPDATE intercity_bookings SET status='NO_SHOW', no_show_at=NOW() WHERE id=$1`, id)
		require.NoError(t, err)
	}

	n, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)
	require.Equal(t, 6, n,
		"only 40%% of seats may be discounted: 10 - 4 = 6 still chargeable")
}

func TestObligations_AreIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 18)
	confirmSeats(t, ctx, repo, tripID, 3)

	first, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)
	second, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)
	require.Equal(t, 3, first)
	require.Equal(t, 0, second, "re-running must create nothing new")

	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM intercity_credit_charges WHERE trip_id=$1`, tripID).Scan(&rows))
	require.Equal(t, 3, rows)
}

// The charge row freezes the entitlement pool at departure, so settlement
// cannot land on a different pool than the publish gate checked.
func TestObligations_FreezeTheVehicleTypePool(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	tripID := insertIntercityTrip(t, ctx, 18)
	confirmSeats(t, ctx, repo, tripID, 1)
	_, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)

	_, expectedVT := intercityTripDriver(t, ctx, tripID)
	var gotVT string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT vehicle_type_id FROM intercity_credit_charges WHERE trip_id=$1 LIMIT 1`,
		tripID).Scan(&gotVT))
	require.Equal(t, expectedVT, gotVT)
}

// --- settlement ------------------------------------------------------------

// The happy path: obligations drain into the ledger exactly once.
func TestSettlement_DrainsObligationsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	ledger := packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop())

	// Settlement is global and oldest-first: drain the backlog BEFORE creating
	// the obligations this test measures.
	for {
		n, _ := repo.SettleCharges(ctx, ledger, 500)
		if n == 0 {
			break
		}
	}

	tripID := insertIntercityTrip(t, ctx, 18)
	driverID, vtID := intercityTripDriver(t, ctx, tripID)
	grantCredits(t, ctx, driverID, vtID, 10, 0)
	confirmSeats(t, ctx, repo, tripID, 3)
	_, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)

	n, err := repo.SettleCharges(ctx, ledger, 500)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	rides, _ := entitlement(t, ctx, driverID, vtID)
	require.Equal(t, 7, rides, "three seats == three credits")

	again, err := repo.SettleCharges(ctx, ledger, 50)
	require.NoError(t, err)
	require.Equal(t, 0, again, "nothing left to settle")
	rides, _ = entitlement(t, ctx, driverID, vtID)
	require.Equal(t, 7, rides, "a second pass must not double-charge")
}

// A driver with no credits does NOT block the trip and does NOT go negative:
// the debt stays PENDING and is collected later.
func TestSettlement_NoCreditsBecomesCollectableDebt(t *testing.T) {
	ctx := context.Background()
	repo := intercity.NewRepository(pool)
	ledger := packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop())

	tripID := insertIntercityTrip(t, ctx, 18)
	driverID, vtID := intercityTripDriver(t, ctx, tripID)
	grantCredits(t, ctx, driverID, vtID, 0, 0)
	confirmSeats(t, ctx, repo, tripID, 2)
	_, err := repo.CreateObligations(ctx, tripID)
	require.NoError(t, err)

	n, err := repo.SettleCharges(ctx, ledger, 50)
	require.NoError(t, err)
	require.Zero(t, n, "nothing could be charged")

	var pending, noCreditAttempts int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(MAX(no_credit_attempts),0) FROM intercity_credit_charges
		 WHERE trip_id=$1 AND status='PENDING'`, tripID).Scan(&pending, &noCreditAttempts))
	require.Equal(t, 2, pending, "the debt survives as PENDING")
	require.GreaterOrEqual(t, noCreditAttempts, 1, "only credit failures count toward arrears")

	rides, bonus := entitlement(t, ctx, driverID, vtID)
	require.Zero(t, rides)
	require.Zero(t, bonus, "balance must NEVER go negative — HasCredits gates going online")

	// Once topped up, the same debt drains.
	grantCredits(t, ctx, driverID, vtID, 5, 0)
	_, err = pool.Exec(ctx,
		`UPDATE intercity_credit_charges SET next_attempt_at = NOW() WHERE trip_id=$1`, tripID)
	require.NoError(t, err)
	n, err = repo.SettleCharges(ctx, ledger, 50)
	require.NoError(t, err)
	require.Equal(t, 2, n, "arrears are COLLECTED, not written off")
}

// --- helpers ---------------------------------------------------------------

func confirmSeats(t *testing.T, ctx context.Context, repo *intercity.Repository, tripID string, seats int) string {
	t.Helper()
	cust := insertIntercityCustomer(t, ctx)
	b, err := repo.Hold(ctx, tripID, cust, seats, uniqueKey("cs"), 30)
	require.NoError(t, err)
	_, err = repo.Confirm(ctx, b.ID, cust)
	require.NoError(t, err)
	return b.ID
}

func grantCredits(t *testing.T, ctx context.Context, driverID, vtID string, rides, bonus int) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO driver_entitlements (driver_id, vehicle_type_id, rides_remaining, bonus_remaining)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (driver_id, vehicle_type_id)
		DO UPDATE SET rides_remaining = driver_entitlements.rides_remaining + EXCLUDED.rides_remaining,
		              bonus_remaining = driver_entitlements.bonus_remaining + EXCLUDED.bonus_remaining`,
		driverID, vtID, rides, bonus)
	require.NoError(t, err)
}
