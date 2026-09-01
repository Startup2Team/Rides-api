//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/ride"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// Service.UpdateVehicle: editing a SAFETY-RELEVANT field (plate number,
// passenger capacity, load capacity) resets that vehicle's approval_status
// back to PENDING_REVIEW so it re-enters admin review, mirroring what
// RejectVehicle/DeleteVehicle already do for the "driver online on a
// suddenly-unapproved active vehicle" eviction case. Cosmetic-only edits
// (make, model, year, color) and no-op resubmits of the same value must NOT
// bounce an already-approved vehicle back into review.
//
// driver.Repository is tied directly to *pgxpool.Pool (no DBTX interface),
// and the reset lives in a raw SQL CASE inside UPDATE (chosen over a
// read-diff-write in Go so it stays atomic against a concurrent PATCH on the
// same vehicle) — so, like the rest of this package's approval-status
// coverage, it can only be proven end-to-end against a real, migrated
// Postgres.

func intPtr(i int) *int { return &i }

// ── Safety-relevant field changed -> reset to PENDING_REVIEW ──────────────

func TestUpdateVehicle_PlateChanged_ResetsToPendingReview(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-plate")

	newPlate := uniquePlate()
	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(newPlate),
	})
	require.NoError(t, err)

	assert.Equal(t, newPlate, updated.PlateNumber)
	assert.Equal(t, driver.VehicleStatusPendingReview, updated.ApprovalStatus,
		"changing the plate on an approved vehicle must reset it to PENDING_REVIEW")
	assert.Nil(t, updated.RejectionReason, "a fresh review cycle must not carry over a stale rejection reason")

	got, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusPendingReview, got.ApprovalStatus, "the reset must be persisted, not just returned")
}

func TestUpdateVehicle_CapacityChanged_ResetsToPendingReview(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, _, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-capacity")
	// setUpApprovedDriverWithOneVehicle's fixture (newKYCApplyInput) never sets
	// passenger_seats, so the seeded vehicle starts with it NULL — setting it
	// for the first time is itself a change (NULL IS DISTINCT FROM 4 is TRUE).
	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PassengerSeats: intPtr(4),
	})
	require.NoError(t, err)

	assert.Equal(t, driver.VehicleStatusPendingReview, updated.ApprovalStatus,
		"changing passenger capacity on an approved vehicle must reset it to PENDING_REVIEW")

	// A second edit to the SAME value must not be treated as a change.
	require.NoError(t, driverRepo.SetVehicleApprovalStatus(ctx, vehicle1.ID, driver.VehicleStatusApproved, nil))
	updated2, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PassengerSeats: intPtr(4),
	})
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusApproved, updated2.ApprovalStatus,
		"resubmitting the SAME passenger_seats value must not be treated as a change")
}

func TestUpdateVehicle_RejectedVehicle_PlateFixed_MovesToPendingReview(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, _, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-rejected")
	require.NoError(t, driverRepo.SetVehicleApprovalStatus(ctx, vehicle1.ID, driver.VehicleStatusRejected, strPtr("blurry plate photo")))

	newPlate := uniquePlate()
	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(newPlate),
	})
	require.NoError(t, err)

	assert.Equal(t, driver.VehicleStatusPendingReview, updated.ApprovalStatus,
		"fixing a rejected vehicle's plate must send it back to PENDING_REVIEW, not leave it stuck REJECTED")
	assert.Nil(t, updated.RejectionReason, "the old rejection reason no longer applies to the edited vehicle")
}

// ── Cosmetic-only / no-op edits must NOT reset approval ────────────────────

func TestUpdateVehicle_CosmeticFieldsOnly_ApprovalUnchanged(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, _, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-cosmetic")

	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		Color: strPtr("Midnight Blue"),
		Make:  strPtr("Toyota"),
		Model: strPtr("Vitz"),
		Year:  intPtr(2019),
	})
	require.NoError(t, err)

	assert.Equal(t, "Midnight Blue", *updated.Color)
	assert.Equal(t, driver.VehicleStatusApproved, updated.ApprovalStatus,
		"a cosmetic-only edit (make/model/year/color) must NOT reset an already-approved vehicle")
}

func TestUpdateVehicle_SamePlateResubmitted_ApprovalUnchanged(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, _, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-noop-plate")

	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(vehicle1.PlateNumber), // resubmitting the exact same value
	})
	require.NoError(t, err)

	assert.Equal(t, driver.VehicleStatusApproved, updated.ApprovalStatus,
		"resubmitting the SAME plate value must not be treated as a change")
}

// ── Active vehicle + online driver: safety edit evicts from matching ──────
//
// Mirrors TestAdminRejectVehicle_ActiveVehicleOfOnlineDriver_ForcesOfflineAndEvictsFromGeoIndex:
// matching only re-checks approval at ActivateVehicle/SetAvailability's entry
// gates, never a driver already pinned in the Redis geo index, so an edit
// that resets the driver's ACTIVE vehicle out from under an online driver
// must force them offline and out of the geo index the same way.

func TestUpdateVehicle_ActiveVehicleOfOnlineDriver_SafetyEdit_EvictsFromMatching(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-evict")

	require.NoError(t, driverRepo.UpdateOnlineStatus(ctx, userID, true))
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverState(profile.ID), "AVAILABLE", 0).Err())
	require.NoError(t, rdb.GeoAdd(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), &goredis.GeoLocation{
		Name: profile.ID, Longitude: 30.0619, Latitude: -1.9441,
	}).Err())

	driverSvcWithRedis := driver.NewService(driverRepo, rdb, nil, &config.Config{}, zerolog.Nop())

	updated, err := driverSvcWithRedis.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(uniquePlate()),
	})
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusPendingReview, updated.ApprovalStatus)

	after, err := driverRepo.FindProfileByUserID(ctx, userID)
	require.NoError(t, err)
	assert.False(t, after.IsOnline,
		"a safety edit that resets the driver's ACTIVE vehicle must force is_online=false")

	state, err := rdb.Get(ctx, rkeys.K.DriverState(profile.ID)).Result()
	require.NoError(t, err)
	assert.Equal(t, "OFFLINE", state)

	_, err = rdb.ZScore(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), profile.ID).Result()
	assert.ErrorIs(t, err, goredis.Nil,
		"the evicted driver must no longer be a member of the matching geo index")
}

// TestUpdateVehicle_NonActiveVehicleOfOnlineDriver_SafetyEdit_DoesNotEvict is
// the non-regression check: editing a vehicle that is NOT the driver's
// active one must leave an online driver exactly as they were.
func TestUpdateVehicle_NonActiveVehicleOfOnlineDriver_SafetyEdit_DoesNotEvict(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-no-evict")
	require.NoError(t, driverRepo.UpdateOnlineStatus(ctx, userID, true))

	vehicle2, err := driverSvc.CreateVehicle(ctx, userID, driver.CreateVehicleInput{
		VehicleTypeCode: "CAB_TAXI",
		PlateNumber:     uniquePlate(),
	})
	require.NoError(t, err)
	require.False(t, vehicle2.IsActive)

	updated, err := driverSvc.UpdateVehicle(ctx, userID, vehicle2.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(uniquePlate()),
	})
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusPendingReview, updated.ApprovalStatus,
		"the edited (non-active) vehicle must still reset its own approval")

	after, err := driverRepo.FindProfileByUserID(ctx, userID)
	require.NoError(t, err)
	assert.True(t, after.IsOnline,
		"editing a NON-active vehicle must not force an online driver offline")

	gotV1, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusApproved, gotV1.ApprovalStatus,
		"the active vehicle's own approval must be untouched by a different vehicle's edit")
	assert.True(t, gotV1.IsActive)
}

// ── Active-ride lock is unchanged by this feature ──────────────────────────

func TestUpdateVehicle_ActiveVehicleOnActiveRide_StillLocked(t *testing.T) {
	ctx := context.Background()
	driverRepo := driver.NewRepository(pool)
	rideRepo := ride.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())

	userID, profile, vehicle1 := setUpApprovedDriverWithOneVehicle(t, ctx, driverRepo, adminSvc, "update-locked")

	authRepo := auth.NewRepository(pool)
	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-locked-cust", "android", nil, nil)
	require.NoError(t, err)
	createTestRide(t, ctx, rideRepo, customer.ID, profile.ID, ride.StatusMatched)

	_, err = driverSvc.UpdateVehicle(ctx, userID, vehicle1.ID, driver.UpdateVehicleInput{
		PlateNumber: strPtr(uniquePlate()),
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "VEHICLE_LOCKED_ON_RIDE", appErr.Code)
	assert.Equal(t, 409, appErr.StatusCode)

	got, err := driverRepo.GetVehicle(ctx, profile.ID, vehicle1.ID)
	require.NoError(t, err)
	assert.Equal(t, driver.VehicleStatusApproved, got.ApprovalStatus,
		"a blocked edit must not have partially applied or reset approval")
}
