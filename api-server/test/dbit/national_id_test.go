//go:build integration

package dbit

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/driver"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// DB-1: national ID one-ID-one-account fraud guard (uq_users_national_id,
// migration 080). These are the tests the unit suite in internal/driver and
// internal/admin can't be: they need the REAL partial unique index to fire a
// REAL 23505, against the REAL migrated schema, inside the REAL transaction
// CreateProfile/UpdateProfileForResubmission/SetDriverNationalID run.

var natIDPlateSeq int64

// uniquePlate/uniqueLicense combine a 9-digit wall-clock slice with a 6+
// digit monotonic counter — a BARE in-process counter (starting at 0 every
// run) collides with rows a PRIOR test run already committed to this same
// persistent database, which a counter alone cannot detect; a BARE timestamp
// truncated to fit the column can collide between two calls issued within the
// same instant. Neither half is truncated away, so both properties hold.
// Mirrors uniqueKey's pattern in harness_test.go (which has no length limit
// to work around).
func uniquePlate() string {
	n := atomic.AddInt64(&natIDPlateSeq, 1)
	// "P" + 9 digits of wall clock + 6 digits of counter = 16 chars, well
	// under vehicle_plate's VARCHAR(20) (migration 002).
	return fmt.Sprintf("P%09d%06d", time.Now().UnixNano()%1_000_000_000, n%1_000_000)
}

// license_number must be EXACTLY 16 digits (migration 002 + the admin
// handler's "must be exactly 16 characters" check) — 10 digits of wall clock
// + 6 digits of counter.
func uniqueLicense() string {
	n := atomic.AddInt64(&natIDPlateSeq, 1)
	return fmt.Sprintf("%010d%06d", time.Now().UnixNano()%10_000_000_000, n%1_000_000)
}

func newDriverApplyInput(t *testing.T, userID string) driver.ApplyInput {
	t.Helper()
	return driver.ApplyInput{
		UserID:        userID,
		TransportType: "MOTO_BIKE",
		VehiclePlate:  uniquePlate(),
		LicenseNumber: uniqueLicense(),
		DateOfBirth:   time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC),
		City:          "Kigali",
		MomoPayCode:   "123456",
		MomoProvider:  "mtn",
		Province:      "Kigali", District: "Gasabo", Sector: "Kimironko",
		Cell: "Kibagabaga", Village: "Village1",
	}
}

// TestNationalID_DuplicateAcrossDrivers_Rejected proves the core fraud guard:
// two DIFFERENT users applying with the SAME (country, number) — the second
// one must be rejected with driver.ErrNationalIDTaken, and the first user's
// captured ID must be completely unaffected.
func TestNationalID_DuplicateAcrossDrivers_Rejected(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	u1, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-1", "android", nil, nil)
	require.NoError(t, err)
	u2, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-2", "android", nil, nil)
	require.NoError(t, err)

	sameID := fmt.Sprintf("11%014d", time.Now().UnixNano()%100000000000000) // 16 digits, RW-shaped, unique per run

	in1 := newDriverApplyInput(t, u1.ID)
	in1.NationalIDCountry = "RW"
	in1.NationalIDNumber = sameID
	profile1, err := driverRepo.CreateProfile(ctx, in1)
	require.NoError(t, err, "first driver's application (fresh national ID) must succeed")
	require.NotNil(t, profile1.NationalIDNumber, "own profile must show the (masked) national ID back")

	in2 := newDriverApplyInput(t, u2.ID)
	in2.NationalIDCountry = "RW"
	in2.NationalIDNumber = sameID // SAME ID as u1 — the ban-evasion / bonus-farming case
	_, err = driverRepo.CreateProfile(ctx, in2)
	require.Error(t, err, "a second account must not be able to register with an already-used national ID")
	require.True(t, errors.Is(err, driver.ErrNationalIDTaken), "expected ErrNationalIDTaken, got: %v", err)

	// The second user must have NO driver_profiles row left behind — the
	// transaction (driver_profiles insert + users update) must have rolled
	// back completely, not partially committed the profile without the ID.
	_, err = driverRepo.FindProfileByUserID(ctx, u2.ID)
	require.ErrorIs(t, err, apperrors.ErrNotFound, "the rejected application must leave no orphaned driver_profiles row")

	// First user's captured ID must be untouched by the failed second attempt.
	profile1Again, err := driverRepo.FindProfileByUserID(ctx, u1.ID)
	require.NoError(t, err)
	require.NotNil(t, profile1Again.NationalIDNumber)
	require.Equal(t, *profile1.NationalIDNumber, *profile1Again.NationalIDNumber)
}

// TestNationalID_SameUserResubmit_IsNoop proves the driver-onboarding capture
// is first-write-wins: a user re-applying (e.g. after a REJECTED profile) with
// the SAME national ID they already have on file must succeed as a no-op, not
// error — resubmitting your own unchanged data is the common case.
func TestNationalID_SameUserResubmit_IsNoop(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-3", "android", nil, nil)
	require.NoError(t, err)

	id := fmt.Sprintf("22%014d", time.Now().UnixNano()%100000000000000)

	in := newDriverApplyInput(t, u.ID)
	in.NationalIDCountry = "RW"
	in.NationalIDNumber = id
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)

	// Reject then resubmit with the SAME ID — must succeed (no-op capture).
	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "REJECTED", "", nil))
	resubmit := newDriverApplyInput(t, u.ID)
	resubmit.VehiclePlate = profile.VehiclePlate // resubmission keeps identity fields realistic but not required
	resubmit.NationalIDCountry = "RW"
	resubmit.NationalIDNumber = id
	require.NoError(t, driverRepo.UpdateProfileForResubmission(ctx, resubmit),
		"resubmitting the SAME national ID a user already has on file must be a no-op, not a conflict")
}

// TestNationalID_AdminSetDriverNationalID_ConflictAcrossAccounts proves the
// admin-only edit path (internal/admin.SetDriverNationalID) also honours the
// same one-ID-one-account guard when an admin tries to ASSIGN an ID that
// another account already has captured.
func TestNationalID_AdminSetDriverNationalID_ConflictAcrossAccounts(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	adminSvc := admin.NewService(pool, zerolog.Nop())

	u1, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-4", "android", nil, nil)
	require.NoError(t, err)
	u2, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-5", "android", nil, nil)
	require.NoError(t, err)

	takenID := fmt.Sprintf("33%014d", time.Now().UnixNano()%100000000000000)

	in1 := newDriverApplyInput(t, u1.ID)
	in1.NationalIDCountry = "RW"
	in1.NationalIDNumber = takenID
	_, err = driverRepo.CreateProfile(ctx, in1)
	require.NoError(t, err)

	// u2 applies with NO national ID, then an admin tries to assign u1's ID.
	in2 := newDriverApplyInput(t, u2.ID)
	profile2, err := driverRepo.CreateProfile(ctx, in2)
	require.NoError(t, err)

	_, _, err = adminSvc.SetDriverNationalID(ctx, profile2.ID, "RW", takenID)
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	require.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}
