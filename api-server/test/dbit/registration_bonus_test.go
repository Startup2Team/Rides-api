//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/bonus"
	"github.com/workspace/ride-platform/internal/packages"
)

// DB-4 regression: ApproveDriver used to grant the "30 free rides" registration
// bonus ONLY into the legacy bonus_grants/driver_ride_credits tables (keyed on
// users.id). The go-online and ride-accept credit gates, and GET /credits +
// /entitlements, read ONLY the v4 ledger (driver_entitlements, keyed on
// driver_profiles.id) — so the bonus was visible in bonus history but could
// never actually be spent. This proves the fix: approving a driver lands the
// bonus in driver_entitlements too, and HasCredits (the exact function both
// spend gates call) reports it as usable.
func TestApproveDriver_RegistrationBonusIsSpendableInV4Ledger(t *testing.T) {
	ctx := context.Background()

	// A REGISTRATION bonus_tiers row (30 rides) is seeded by migration 040 and
	// a vehicle_types row for MOTO_BIKE by an earlier migration — both are part
	// of the real schema this test runs migrations against, not fixtures here.

	phone := uniquePhone()
	driverUser, err := auth.NewRepository(pool).CreateUser(ctx, phone, "dev-regbonus", "android", nil, nil)
	require.NoError(t, err)

	adminUserID := insertAdminAccount(t, ctx, "reg-bonus-admin-"+uniqueKey("a")+"@rides.test")

	// Insert a PENDING_REVIEW driver_profiles row directly — ApproveDriver's
	// UPDATE only transitions rows that are still pending.
	key := uniqueKey("d")
	var profileID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_profiles
		    (user_id, transport_type, vehicle_plate, license_number, date_of_birth, city, momo_pay_code, approval_status)
		VALUES ($1, 'MOTO_BIKE', $2, $3, '1995-01-01', 'Kigali', '+250788000000', 'PENDING_REVIEW')
		RETURNING id`,
		driverUser.ID, "RA "+key[len(key)-6:], "DL-"+key).Scan(&profileID))

	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = 'MOTO_BIKE'`).Scan(&vehicleTypeID))

	bonusRepo := bonus.NewRepository(pool)
	bonusSvc := bonus.NewService(bonusRepo, zerolog.Nop())
	pkgRepo := packages.NewRepository(pool)
	ledgerSvc := packages.NewLedgerService(pkgRepo, zerolog.Nop())

	adminSvc := admin.NewService(pool, zerolog.Nop())
	adminSvc.SetBonusService(bonusSvc)
	adminSvc.SetPackagesService(ledgerSvc)

	// Before approval: no credits, nothing spendable.
	hasCredits, err := ledgerSvc.HasCredits(ctx, driverUser.ID, "MOTO_BIKE")
	require.NoError(t, err)
	require.False(t, hasCredits, "an unapproved driver must not have spendable credits")

	require.NoError(t, adminSvc.ApproveDriver(ctx, profileID, adminUserID))

	// The legacy bonus_grants row still exists (bonus history continuity).
	var bonusGrantsCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM bonus_grants WHERE driver_id = $1`, driverUser.ID).Scan(&bonusGrantsCount))
	require.Equal(t, 1, bonusGrantsCount, "bonus history (GET /bonuses) must keep working")

	// The fix: the v4 ledger (driver_entitlements) also has the bonus, keyed on
	// driver_profiles.id — this is what the go-online/accept gates actually read.
	// ApproveDriver also grants a free-trial package on the same vehicle type
	// (a separate, pre-existing v4 path — see GrantFreeTrialIfEligible), so the
	// cached bonus_remaining is free-trial-bonus + registration-bonus; the exact
	// registration-only amount is asserted below via its own ledger row.
	var bonusRemaining int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2`, profileID, vehicleTypeID,
	).Scan(&bonusRemaining))
	require.GreaterOrEqual(t, bonusRemaining, 30, "the 30-ride registration bonus must land in driver_entitlements")

	// And it is spendable: HasCredits is the exact call both the go-online gate
	// (driver/service.go) and the ride-accept gate (main.go driverAcceptHandler)
	// make before letting a driver go online / accept a ride.
	hasCredits, err = ledgerSvc.HasCredits(ctx, driverUser.ID, "MOTO_BIKE")
	require.NoError(t, err)
	require.True(t, hasCredits, "registration bonus must be spendable at the go-online/accept gate")

	// A ledger entry was recorded for it (append-only proof, not just the cache).
	var ledgerBonusDelta int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT bonus_delta FROM ride_credit_ledger
		WHERE driver_id = $1 AND entry_type = 'BONUS_GRANT' AND idempotency_key = $2`,
		profileID, "registration:"+profileID,
	).Scan(&ledgerBonusDelta))
	require.Equal(t, 30, ledgerBonusDelta)
}

// Idempotency: re-granting under the same registration idempotency key must
// not double the driver's balance (e.g. a retried/duplicate admin approval).
func TestGrantRegistrationBonus_IsIdempotent(t *testing.T) {
	ctx := context.Background()

	phone := uniquePhone()
	driverUser, err := auth.NewRepository(pool).CreateUser(ctx, phone, "dev-regbonus-idem", "android", nil, nil)
	require.NoError(t, err)

	profileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")
	var vehicleTypeID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM vehicle_types WHERE code = 'MOTO_BIKE'`).Scan(&vehicleTypeID))

	pkgRepo := packages.NewRepository(pool)
	ledgerSvc := packages.NewLedgerService(pkgRepo, zerolog.Nop())

	require.NoError(t, ledgerSvc.GrantRegistrationBonus(ctx, driverUser.ID, vehicleTypeID, 30))
	require.NoError(t, ledgerSvc.GrantRegistrationBonus(ctx, driverUser.ID, vehicleTypeID, 30)) // retry

	var bonusRemaining int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2`, profileID, vehicleTypeID,
	).Scan(&bonusRemaining))
	require.Equal(t, 30, bonusRemaining, "a retried grant under the same idempotency key must not double the balance")

	var ledgerRows int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_credit_ledger
		WHERE driver_id = $1 AND idempotency_key = $2`, profileID, "registration:"+profileID,
	).Scan(&ledgerRows))
	require.Equal(t, 1, ledgerRows, "exactly one ledger entry must exist per driver despite the retry")
}

// insertAdminAccount creates a minimally-valid admin_accounts row (approved_by
// on driver_profiles FKs to admin_accounts, not users) and returns its id.
func insertAdminAccount(t *testing.T, ctx context.Context, email string) string {
	t.Helper()
	var roleID, adminID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM admin_roles WHERE name = 'Super Admin'`).Scan(&roleID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO admin_accounts (name, email, role_id, status)
		VALUES ('Test Admin', $1, $2, 'ACTIVE')
		RETURNING id`, email, roleID).Scan(&adminID))
	return adminID
}
