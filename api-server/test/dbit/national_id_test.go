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

	"github.com/workspace/ride-platform/config"
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

// createTestAdminAccount inserts a minimal admin_accounts row (Super Admin
// role) and returns its id — driver_profiles.approved_by references
// admin_accounts(id), NOT users(id) (migration 042), so ApproveDriver needs a
// real admin_accounts row to satisfy that foreign key, not a regular user.
func createTestAdminAccount(t *testing.T, ctx context.Context) string {
	t.Helper()
	var roleID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM admin_roles WHERE name = 'Super Admin'`,
	).Scan(&roleID))

	var adminID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO admin_accounts (name, email, role_id)
		VALUES ($1, $2, $3)
		RETURNING id
	`, "Test Admin", fmt.Sprintf("test-admin-%d@example.com", time.Now().UnixNano()), roleID).Scan(&adminID))
	return adminID
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
	require.NotNil(t, profile1.NationalIDNumber, "own profile must show the national ID back")
	require.Equal(t, sameID, *profile1.NationalIDNumber,
		"DB-1 round 2: FindProfileByUserID returns the FULL number for the owner's own profile, not masked")

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

// TestNationalID_RejectedResubmit_CorrectsID proves the DB-1 round 2 fix to
// the "silent-resubmit" bug: a REJECTED driver resubmitting with a
// DIFFERENT (corrected) national ID must have the correction actually land —
// previously (first-write-wins on this path too) it was silently dropped
// because a value already existed on file.
func TestNationalID_RejectedResubmit_CorrectsID(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-10", "android", nil, nil)
	require.NoError(t, err)

	typoID := fmt.Sprintf("12%014d", time.Now().UnixNano()%100000000000000)
	in := newDriverApplyInput(t, u.ID)
	in.NationalIDCountry = "RW"
	in.NationalIDNumber = typoID
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)

	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "REJECTED", "", nil))

	correctedID := fmt.Sprintf("13%014d", time.Now().UnixNano()%100000000000000)
	resubmit := newDriverApplyInput(t, u.ID)
	resubmit.VehiclePlate = profile.VehiclePlate
	resubmit.NationalIDCountry = "RW"
	resubmit.NationalIDNumber = correctedID
	require.NoError(t, driverRepo.UpdateProfileForResubmission(ctx, resubmit))

	updated, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.NationalIDNumber)
	require.Equal(t, correctedID, *updated.NationalIDNumber,
		"the corrected ID must overwrite the typo'd one, not be silently dropped")
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

	_, _, _, err = adminSvc.SetDriverNationalID(ctx, profile2.ID, "RW", takenID)
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	require.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

// TestNationalID_OwnerCanEditWhilePending proves the DB-1 round 2
// self-correction path: a driver whose approval is still PENDING_REVIEW can
// overwrite their own national ID via driver.Service.SetOwnNationalID, and
// FindProfileByUserID reflects the FULL corrected number afterward (their own
// profile is never masked).
func TestNationalID_OwnerCanEditWhilePending(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-6", "android", nil, nil)
	require.NoError(t, err)

	originalID := fmt.Sprintf("44%014d", time.Now().UnixNano()%100000000000000)
	in := newDriverApplyInput(t, u.ID)
	in.NationalIDCountry = "RW"
	in.NationalIDNumber = originalID
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", profile.ApprovalStatus)

	correctedID := fmt.Sprintf("55%014d", time.Now().UnixNano()%100000000000000)
	err = driverSvc.SetOwnNationalID(ctx, u.ID, "RW", correctedID)
	require.NoError(t, err, "a PENDING_REVIEW driver must be able to correct their own national ID")

	updated, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.NationalIDNumber)
	require.Equal(t, correctedID, *updated.NationalIDNumber,
		"the correction must actually overwrite the original value, not silently no-op")
}

// TestNationalID_LocksAfterApproval proves the other half of the same fix: once
// APPROVED, SetOwnNationalID refuses the driver's own correction attempt.
func TestNationalID_LocksAfterApproval(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	adminSvc := admin.NewService(pool, zerolog.Nop())
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-7", "android", nil, nil)
	require.NoError(t, err)

	originalID := fmt.Sprintf("66%014d", time.Now().UnixNano()%100000000000000)
	in := newDriverApplyInput(t, u.ID)
	in.NationalIDCountry = "RW"
	in.NationalIDNumber = originalID
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)

	// Approve directly (bypassing the admin HTTP layer, but exercising the same
	// admin.Service.ApproveDriver the route calls) — a real admin_accounts row,
	// since driver_profiles.approved_by references admin_accounts, not users.
	approverID := createTestAdminAccount(t, ctx)
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))

	attemptedID := fmt.Sprintf("77%014d", time.Now().UnixNano()%100000000000000)
	err = driverSvc.SetOwnNationalID(ctx, u.ID, "RW", attemptedID)
	require.Error(t, err, "an APPROVED driver must not be able to self-correct their national ID")
	require.ErrorIs(t, err, driver.ErrNationalIDLocked)

	// The original value must be untouched by the rejected attempt.
	unchanged, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.NationalIDNumber)
	require.Equal(t, originalID, *unchanged.NationalIDNumber)
}

// TestNationalID_RepoSetOwnNationalID_AtomicGuardRejectsAfterApproval proves
// the TOCTOU fix directly against the repository method, not the service: it
// calls driver.Repository.SetOwnNationalID straight after approval, WITHOUT
// any caller-side pre-read of approval_status first — i.e. exactly the shape
// of the old bug window (a read that's already gone stale by the time the
// write runs). Before the fix, this repository method wrote unconditionally
// and only the service's separate pre-read stood between a driver and
// changing their ID post-approval; a concurrent ApproveDriver landing between
// that read and this write let the change through anyway. Now the guard is
// IN this statement (UPDATE ... FROM driver_profiles ... WHERE
// approval_status = ANY(editable)), so it refuses the write on its own,
// regardless of what any caller checked beforehand.
func TestNationalID_RepoSetOwnNationalID_AtomicGuardRejectsAfterApproval(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	adminSvc := admin.NewService(pool, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nid-11", "android", nil, nil)
	require.NoError(t, err)

	originalID := fmt.Sprintf("11%014d", time.Now().UnixNano()%100000000000000)
	in := newDriverApplyInput(t, u.ID)
	in.NationalIDCountry = "RW"
	in.NationalIDNumber = originalID
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", profile.ApprovalStatus)

	approverID := createTestAdminAccount(t, ctx)
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))

	// Call the REPOSITORY method directly, bypassing the service layer (and
	// therefore any pre-read it might have done) entirely.
	attemptedID := fmt.Sprintf("12%014d", time.Now().UnixNano()%100000000000000)
	err = driverRepo.SetOwnNationalID(ctx, u.ID, "RW", attemptedID)
	require.Error(t, err, "the atomic guard must reject the write even with no prior approval_status check by the caller")
	require.ErrorIs(t, err, driver.ErrNationalIDLocked)

	unchanged, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.NationalIDNumber)
	require.Equal(t, originalID, *unchanged.NationalIDNumber,
		"the original ID must be completely untouched by the rejected write")
}

// TestNationalID_AdminCreateAtomicity_ProfileInsertFailureLeavesNoIDBound
// proves the DB-1 round 2 atomicity fix against a REAL Postgres transaction
// (not a mock): if CreateDriverFromAdmin's driver_profiles insert fails (a
// duplicate vehicle plate here), the national-ID capture and the user
// creation must roll back too — no phantom user left permanently owning a
// real person's national ID with no driver record to show for it.
func TestNationalID_AdminCreateAtomicity_ProfileInsertFailureLeavesNoIDBound(t *testing.T) {
	ctx := context.Background()
	adminSvc := admin.NewService(pool, zerolog.Nop())
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	// driver_profiles.approved_by references admin_accounts(id), not
	// users(id) — a real admin_accounts row, same as TestNationalID_LocksAfterApproval.
	adminAccountID := createTestAdminAccount(t, ctx)

	sharedPlate := uniquePlate()

	// First registration takes the plate (succeeds, uninteresting on its own).
	firstID := fmt.Sprintf("88%014d", time.Now().UnixNano()%100000000000000)
	_, err := adminSvc.CreateDriverFromAdmin(ctx, admin.AdminCreateDriverInput{
		AdminUserID: adminAccountID, FullName: "First Driver", Phone: uniquePhone(),
		TransportType: "MOTO_BIKE", VehiclePlate: sharedPlate, LicenseNumber: uniqueLicense(),
		Province: "Kigali", District: "Gasabo", Sector: "Kimironko", Cell: "Kibagabaga", Village: "Village1",
		MomoPayCode:       "123456",
		NationalIDCountry: "RW", NationalIDNumber: firstID,
	})
	require.NoError(t, err)

	// Second registration: a brand-new phone/user, a FRESH national ID, but the
	// SAME (already-taken) plate — the driver_profiles insert must fail on the
	// plate, and the whole transaction (including the new user + their ID
	// capture) must roll back.
	secondPhone := uniquePhone()
	secondID := fmt.Sprintf("99%014d", time.Now().UnixNano()%100000000000000)
	_, err = adminSvc.CreateDriverFromAdmin(ctx, admin.AdminCreateDriverInput{
		AdminUserID: adminAccountID, FullName: "Second Driver", Phone: secondPhone,
		TransportType: "MOTO_BIKE", VehiclePlate: sharedPlate, LicenseNumber: uniqueLicense(),
		Province: "Kigali", District: "Gasabo", Sector: "Kimironko", Cell: "Kibagabaga", Village: "Village1",
		MomoPayCode:       "123456",
		NationalIDCountry: "RW", NationalIDNumber: secondID,
	})
	require.Error(t, err, "a duplicate plate must fail the registration")

	// The phantom user must not exist bound to secondID at all — either the
	// user row itself doesn't exist (rolled back), or if the phone lookup
	// somehow found a row, it must NOT carry secondID.
	secondUser, findErr := authRepo.FindUserByPhone(ctx, secondPhone)
	if findErr == nil && secondUser != nil {
		p, perr := driverRepo.FindProfileByUserID(ctx, secondUser.ID)
		require.Error(t, perr, "no driver_profiles row should exist for the rolled-back registration")
		_ = p
	}

	// And the ID itself must not be bound to ANYONE — proving the capture
	// rolled back, not just the profile insert.
	var boundCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE national_id_number = $1`, secondID,
	).Scan(&boundCount))
	require.Zero(t, boundCount, "the rolled-back registration's national ID must not be bound to any account")
}
