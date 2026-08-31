//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/pkg/documents"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Per-vehicle approval (migration 089). These are the tests
// internal/driver's unit suite can't be — driver.Repository is tied directly
// to *pgxpool.Pool (no DBTX interface), so ActivateVehicle's gate and
// UploadDocument's scoped reopen can only be proven end-to-end against a
// real, migrated Postgres.
//
// The product rule under test: a driver owns several vehicles but drives
// exactly one at a time, and adding/reviewing a NEW vehicle must never
// disturb the vehicle they are already approved and earning on.

// setUpApprovedDriverWithOneVehicle creates a user, applies (which mirrors
// the application's vehicle into driver_vehicles as vehicle #1, PENDING_
// REVIEW), then has an admin approve the driver — which must also sync
// vehicle #1 to APPROVED (the fix in admin.Service.ApproveDriver) so this
// helper's callers start from the exact "driver approved and already working
// on vehicle #1" state the whole feature is built around.
func setUpApprovedDriverWithOneVehicle(t *testing.T, ctx context.Context, driverRepo *driver.Repository, adminSvc *admin.Service, tag string) (userID string, profile *driver.Profile, vehicle1 *driver.Vehicle) {
	t.Helper()
	authRepo := auth.NewRepository(pool)

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-"+tag, "android", nil, nil)
	require.NoError(t, err)

	p, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.NoError(t, driverRepo.CreateVehicleFromApply(ctx, p.ID, newKYCApplyInput(t, u.ID)))

	approverID := createTestAdminAccount(t, ctx)
	require.NoError(t, adminSvc.ApproveDriver(ctx, p.ID, approverID))

	vehicles, err := driverRepo.ListVehicles(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, vehicles, 1, "setup: driver must start with exactly one vehicle")
	require.Equal(t, driver.VehicleStatusApproved, vehicles[0].ApprovalStatus,
		"setup: admin.ApproveDriver must sync the driver's active vehicle to APPROVED")
	require.True(t, vehicles[0].IsActive)

	after, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "APPROVED", after.ApprovalStatus)

	return u.ID, after, vehicles[0]
}

// ── ActivateVehicle: rejects an unapproved target vehicle ─────────────────

func TestActivateVehicle_UnapprovedVehicle_Rejected(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "activate-1")

	// Driver adds a second vehicle — born PENDING_REVIEW (the column default),
	// not active (CreateVehicle only auto-activates a driver's FIRST vehicle).
	vehicle2, err := driverSvc.CreateVehicle(ctx, userID, driver.CreateVehicleInput{
		VehicleTypeCode: "CAB_TAXI",
		PlateNumber:     uniquePlate(),
	})
	require.NoError(t, err)
	require.Equal(t, driver.VehicleStatusPendingReview, vehicle2.ApprovalStatus)
	require.False(t, vehicle2.IsActive)

	_, err = driverSvc.ActivateVehicle(ctx, userID, vehicle2.ID)
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "VEHICLE_NOT_APPROVED", appErr.Code)
	assert.Equal(t, 409, appErr.StatusCode)

	// Vehicle #1 must still be the active one — a rejected switch must leave
	// exactly one vehicle active, never zero.
	got1, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.True(t, got1.IsActive, "the rejected switch must not have disturbed the currently active vehicle")

	got2, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle2.ID)
	require.NoError(t, err)
	assert.False(t, got2.IsActive, "the unapproved vehicle must not have become active")
}

// ── UploadDocument: a non-active vehicle's document reopens ONLY that
// vehicle's review, leaving the driver profile and online status untouched ─

func TestUploadDocument_NonActiveVehicle_ScopedReopen_DriverStaysOnlineAndApproved(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "scoped-1")
	approverID := createTestAdminAccount(t, ctx)

	// Driver is online, actively working on vehicle #1.
	require.NoError(t, driverRepo.UpdateOnlineStatus(ctx, userID, true))

	// Adds and gets vehicle #2 approved too (e.g. approved on a previous
	// review pass), so a fresh document upload for it has something to
	// meaningfully reopen — PENDING_REVIEW is already where it needs to be.
	vehicle2, err := driverSvc.CreateVehicle(ctx, userID, driver.CreateVehicleInput{
		VehicleTypeCode: "CAB_TAXI",
		PlateNumber:     uniquePlate(),
	})
	require.NoError(t, err)
	require.NoError(t, adminSvc.ApproveVehicle(ctx, profile.ID, vehicle2.ID, approverID))

	// Driver replaces vehicle #2's insurance document — vehicle #2 is NOT the
	// active vehicle (vehicle #1 still is).
	err = driverSvc.UploadDocument(ctx, userID, documents.VehicleInsurance,
		"https://example.com/insurance-v2.jpg", "", &vehicle2.ID)
	require.NoError(t, err)

	// Vehicle #2 alone goes back under review.
	gotV2, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle2.ID)
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusPendingReview, gotV2.ApprovalStatus,
		"uploading a document for a non-active vehicle must reopen THAT vehicle's review")

	// Vehicle #1 (active, currently worked) is completely untouched.
	gotV1, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusApproved, gotV1.ApprovalStatus,
		"the active vehicle's approval must not be touched by a different vehicle's document upload")
	assert.True(t, gotV1.IsActive)

	// The driver's own profile approval and online status must be untouched —
	// this is the whole point of scoping: they keep earning on vehicle #1
	// while vehicle #2 is reviewed independently.
	afterProfile, err := driverRepo.FindProfileByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "APPROVED", afterProfile.ApprovalStatus,
		"a non-active vehicle's document upload must NOT reopen the whole driver for review")
	assert.True(t, afterProfile.IsOnline,
		"a non-active vehicle's document upload must NOT force the driver offline")
}

// TestUploadDocument_ActiveVehicleDocument_StillReopensWholeDriver is a
// non-regression check: a document for the vehicle the driver IS currently
// working on must keep today's whole-driver behaviour (profile back to
// PENDING_REVIEW, forced offline) — only a NON-active vehicle's document
// gets the new scoped treatment.
func TestUploadDocument_ActiveVehicleDocument_StillReopensWholeDriver(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, _, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "active-doc-1")
	require.NoError(t, driverRepo.UpdateOnlineStatus(ctx, userID, true))

	err := driverSvc.UploadDocument(ctx, userID, documents.VehicleInsurance,
		"https://example.com/insurance-v1-renewed.jpg", "", &vehicle1.ID)
	require.NoError(t, err)

	after, err := driverRepo.FindProfileByUserID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "PENDING_REVIEW", after.ApprovalStatus,
		"a document for the ACTIVE vehicle must still reopen the whole driver for review")
	assert.False(t, after.IsOnline,
		"a document for the ACTIVE vehicle must still force the driver offline (unchanged pre-existing behaviour)")
}

// ── Admin approve → driver can then activate ───────────────────────────────

func TestAdminApproveVehicle_ThenDriverCanActivateIt(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "approve-then-activate")
	approverID := createTestAdminAccount(t, ctx)

	vehicle2, err := driverSvc.CreateVehicle(ctx, userID, driver.CreateVehicleInput{
		VehicleTypeCode: "CAB_TAXI",
		PlateNumber:     uniquePlate(),
	})
	require.NoError(t, err)

	// Confirmed blocked before approval (mirrors TestActivateVehicle_UnapprovedVehicle_Rejected).
	_, err = driverSvc.ActivateVehicle(ctx, userID, vehicle2.ID)
	require.Error(t, err)

	require.NoError(t, adminSvc.ApproveVehicle(ctx, profile.ID, vehicle2.ID, approverID))

	gotV2, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle2.ID)
	require.NoError(t, err)
	require.Equal(t, driver.VehicleStatusApproved, gotV2.ApprovalStatus)

	activated, err := driverSvc.ActivateVehicle(ctx, userID, vehicle2.ID)
	require.NoError(t, err, "an approved vehicle must now activate successfully")
	assert.True(t, activated.IsActive)

	// Exactly one vehicle active — vehicle #1 must have been deactivated, not
	// left active alongside vehicle #2.
	gotV1, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.False(t, gotV1.IsActive)
	// Vehicle #1's OWN approval is untouched by the switch — it is still
	// APPROVED, just no longer the active one.
	assert.Equal(t, driver.VehicleStatusApproved, gotV1.ApprovalStatus)
}

// TestAdminRejectVehicle_IDOR proves the admin reject/approve endpoints
// cannot be used to mutate a vehicle belonging to a DIFFERENT driver — the
// WHERE id = $1 AND driver_id = $2 guard (this codebase's existing IDOR
// pattern, see VehicleBelongsToDriver / resolveVehicleForDocument) must
// report 404, not silently no-op or succeed.
func TestAdminApproveVehicle_WrongDriver_NotFound(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	adminSvc := admin.NewService(pool, zerolog.Nop())

	_, profileA, _ := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "idor-a")
	_, _, vehicleB := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "idor-b")
	approverID := createTestAdminAccount(t, ctx)

	// vehicleB belongs to driver B, not driver A — approving it "as driver A's
	// vehicle" must not succeed.
	err := adminSvc.ApproveVehicle(ctx, profileA.ID, vehicleB.ID, approverID)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}
