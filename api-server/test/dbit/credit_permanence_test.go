//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/packages"
)

// Regression test for commit 74ed405 ("credits are permanent — remove 30-day
// expiry + buggy expiry sweep"). Before that fix, grant() stamped ride_credit_
// ledger rows with expires_at = now() + 30 days, and an hourly SweepExpired
// worker re-derived "used" from ALL negative ledger deltas (including its own
// prior EXPIRY rows), draining a driver's balance to zero over time. The fix
// dropped the expiresAt param from grant()/GrantPurchase entirely and deleted
// the sweep. This test locks in that purchase grants are written permanent
// (expires_at IS NULL) and that the entitlement cache reflects the full
// granted amount — nothing shaved by an expiry that must never run again.
func TestGrantPurchase_CreditsArePermanent(t *testing.T) {
	ctx := context.Background()

	driverUser, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-perm", "android", nil, nil)
	require.NoError(t, err)
	profileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = 'MOTO_BIKE'`).Scan(&vehicleTypeID))

	// source_purchase_id references package_purchases(id); package_id/
	// package_version_id are frozen-snapshot plain values (no FK — see
	// migration 047), so gen_random_uuid() satisfies the NOT NULL columns
	// without needing a real catalog row.
	const rides, bonus = 10, 3
	var purchaseID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO package_purchases
		    (driver_id, vehicle_type_id, package_id, package_version_id, package_version_number,
		     package_name, price_paid_rwf, rides_granted, bonus_rides_granted, payment_ref, idempotency_key)
		VALUES ($1, $2, gen_random_uuid(), gen_random_uuid(), 1,
		        'Test Package', 0, $3, $4, $5, $6)
		RETURNING id
	`, profileID, vehicleTypeID, rides, bonus, uniqueKey("payref"), uniqueKey("purchidem")).Scan(&purchaseID))

	ledgerSvc := packages.NewLedgerService(packages.NewRepository(pool), zerolog.Nop())
	require.NoError(t, ledgerSvc.GrantPurchase(ctx, profileID, nil, vehicleTypeID, purchaseID, rides, bonus))

	// 1. Both the paid-rides grant and the bonus grant must be permanent.
	var grantExpiresAt, bonusExpiresAt *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT expires_at FROM ride_credit_ledger WHERE idempotency_key = $1`,
		"grant:"+purchaseID).Scan(&grantExpiresAt))
	require.Nil(t, grantExpiresAt, "purchase grant must be written with expires_at = NULL (permanent)")

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT expires_at FROM ride_credit_ledger WHERE idempotency_key = $1`,
		"bonus:"+purchaseID).Scan(&bonusExpiresAt))
	require.Nil(t, bonusExpiresAt, "bonus grant must be written with expires_at = NULL (permanent)")

	// 2. The entitlement cache reflects the full granted amount — nothing
	// shaved by an expiry sweep.
	var ridesRemaining, bonusRemaining int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2
	`, profileID, vehicleTypeID).Scan(&ridesRemaining, &bonusRemaining))
	require.Equal(t, rides, ridesRemaining, "rides_remaining must equal the full granted amount")
	require.Equal(t, bonus, bonusRemaining, "bonus_remaining must equal the full granted amount")
}
