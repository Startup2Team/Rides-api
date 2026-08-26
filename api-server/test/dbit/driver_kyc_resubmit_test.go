//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/pkg/documents"
)

// These are the tests the unit suite in internal/driver/internal/admin can't
// be: driver.Repository is tied directly to *pgxpool.Pool (no DBTX
// interface), so the reject→resubmit→admin-review loop can only be proven
// end-to-end against a real, migrated Postgres — the SAME repository methods
// (SetApprovalStatus, UpdateProfileForResubmission, UpsertDocument) run in
// production.
//
// The bug this covers: a hardcoded "PENDING" (missing "_REVIEW") in
// UploadDocument, and Apply's resubmission branch only recognizing REJECTED
// (not NEEDS_MORE_INFO), together meant a driver who fixed their documents
// after either rejection path could vanish from the admin queue forever —
// their profile said a status the admin's WHERE approval_status =
// 'PENDING_REVIEW' query never matched.

func newKYCApplyInput(t *testing.T, userID string) driver.ApplyInput {
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

// ── UploadDocument: reopens review from every status that should reopen ───

func TestUploadDocument_ResubmitFromRejected_ReopensReviewAndBumpsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-1", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "REJECTED", "", strPtr("blurry photo")))

	before, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "REJECTED", before.ApprovalStatus)

	// Resubmitting via /documents (not /apply) must be enough on its own to
	// reopen review — this is the exact loop that was broken.
	err = driverSvc.UploadDocument(ctx, u.ID, documents.Selfie, "https://example.com/selfie2.jpg", "", nil)
	require.NoError(t, err)

	after, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", after.ApprovalStatus,
		"a REJECTED driver's document upload must reopen review — this is the reject/resubmit loop the fix closes")
	require.True(t, after.UpdatedAt.After(before.UpdatedAt), "updated_at must be bumped so the driver resurfaces at the top of the admin queue")
	require.Nil(t, after.RejectionReason, "a stale rejection reason must not linger once the driver has resubmitted")

	// And the admin queue's own status query must actually find them again —
	// this is the query the reviewer's UI runs.
	adminSvc := admin.NewService(pool, zerolog.Nop())
	rows, total, err := adminSvc.ListDrivers(ctx, "PENDING_REVIEW", "", "", "", 100, 0)
	require.NoError(t, err)
	require.Greater(t, total, 0)
	require.True(t, containsDriverID(rows, profile.ID), "resubmitted driver must appear in the PENDING_REVIEW admin queue")
}

func TestUploadDocument_ResubmitFromNeedsMoreInfo_ReopensReview(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-2", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "NEEDS_MORE_INFO", "", strPtr("insurance illegible")))

	err = driverSvc.UploadDocument(ctx, u.ID, documents.Selfie, "https://example.com/selfie2.jpg", "", nil)
	require.NoError(t, err)

	after, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", after.ApprovalStatus,
		"a NEEDS_MORE_INFO driver's document upload must reopen review")
}

func TestUploadDocument_ResubmitFromApproved_ReopensReviewAsPendingReview(t *testing.T) {
	// Regression test for the exact root-cause bug: this branch used to write
	// the literal "PENDING" (missing "_REVIEW"), which no admin query matches.
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-3", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	adminSvc := admin.NewService(pool, zerolog.Nop())
	approverID := createTestAdminAccount(t, ctx)
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))

	err = driverSvc.UploadDocument(ctx, u.ID, documents.Selfie, "https://example.com/selfie2.jpg", "", nil)
	require.NoError(t, err)

	after, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", after.ApprovalStatus,
		`must be exactly "PENDING_REVIEW", not the old "PENDING" typo — admin.ApproveDriver/RejectDriver both gate on approval_status = 'PENDING_REVIEW'`)

	// The re-opened profile must actually be actionable by ApproveDriver again.
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))
	final, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "APPROVED", final.ApprovalStatus)
}

func TestUploadDocument_ResubmitFromApproved_ForcesOnlineDriverOffline(t *testing.T) {
	// LOW-1 (security-review hardening): an APPROVED, currently-online driver
	// who swaps a document is reopened for review, but the approval_status
	// change alone only gates FUTURE go-online calls (SetAvailability
	// requires APPROVED) — it does nothing about a session that is already
	// online. Without forcing is_online back to false here, that driver keeps
	// a ghost pin on the customer nearby-map: the matching engine's dispatch
	// path is approval-gated so they'd get no offers, but Postgres and the
	// Redis geo index would disagree about their availability, which is
	// exactly the drift this platform must never have. The Redis half of
	// this eviction (DriverState + geo index ZRem) is unit-tested in
	// isolation in internal/driver/reopen_review_test.go, since
	// driver.Repository is tied directly to *pgxpool.Pool and cannot be
	// unit-tested — this proves the Postgres half, which the Redis test can't.
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-online", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	adminSvc := admin.NewService(pool, zerolog.Nop())
	approverID := createTestAdminAccount(t, ctx)
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))

	// Simulate the driver going online while APPROVED — UpdateOnlineStatus is
	// the same repository write SetAvailability performs; its own gating
	// (credits, expiry checks) is out of scope here.
	require.NoError(t, driverRepo.UpdateOnlineStatus(ctx, u.ID, true))
	online, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, online.IsOnline, "setup: driver must be online before resubmitting")

	err = driverSvc.UploadDocument(ctx, u.ID, documents.Selfie, "https://example.com/selfie3.jpg", "", nil)
	require.NoError(t, err)

	after, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.False(t, after.IsOnline, "reopening review from APPROVED must force the driver offline in Postgres")
}

// ── Apply: re-/apply resubmission from REJECTED and NEEDS_MORE_INFO ───────

func TestApply_ResubmitFromRejected_ReturnsPendingReview_NoError(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-4", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "REJECTED", "", strPtr("wrong plate")))

	resubmit := newKYCApplyInput(t, u.ID)
	resubmit.VehiclePlate = uniquePlate() // corrected plate
	got, err := driverSvc.Apply(ctx, resubmit)
	require.NoError(t, err, "a second /apply from REJECTED must NOT error")
	require.Equal(t, "PENDING_REVIEW", got.ApprovalStatus)
}

func TestApply_ResubmitFromNeedsMoreInfo_ReturnsPendingReview_NoError(t *testing.T) {
	// Before the fix: Apply only special-cased REJECTED, so a second /apply
	// from NEEDS_MORE_INFO fell through to apperrors.ErrDriverAlreadyApplied
	// (409) — the driver was stuck with no way to resubmit via /apply at all.
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-5", "android", nil, nil)
	require.NoError(t, err)

	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.NoError(t, driverRepo.SetApprovalStatus(ctx, profile.ID, "NEEDS_MORE_INFO", "", strPtr("clarify address")))

	resubmit := newKYCApplyInput(t, u.ID)
	got, err := driverSvc.Apply(ctx, resubmit)
	require.NoError(t, err, "a second /apply from NEEDS_MORE_INFO must NOT error (was ErrDriverAlreadyApplied before this fix)")
	require.Equal(t, "PENDING_REVIEW", got.ApprovalStatus)

	final, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", final.ApprovalStatus)

	var roleState string
	require.NoError(t, pool.QueryRow(ctx, `SELECT role_state FROM users WHERE id = $1`, u.ID).Scan(&roleState))
	require.Equal(t, "DRIVER_PENDING", roleState, "role_state must mirror the reopened review, not stay stuck on whatever it was under NEEDS_MORE_INFO")
}

// ── RequestDriverMoreInfo: full admin→driver→admin loop ───────────────────

func TestRequestDriverMoreInfo_NotifiesAndDriverCanResubmit(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)
	driverSvc := driver.NewService(driverRepo, nil, nil, &config.Config{}, zerolog.Nop())
	adminSvc := admin.NewService(pool, zerolog.Nop())
	approverID := createTestAdminAccount(t, ctx)

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-kyc-6", "android", nil, nil)
	require.NoError(t, err)
	profile, err := driverRepo.CreateProfile(ctx, newKYCApplyInput(t, u.ID))
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", profile.ApprovalStatus)

	err = adminSvc.RequestDriverMoreInfo(ctx, profile.ID, approverID, "insurance photo unreadable", nil)
	require.NoError(t, err)

	afterMoreInfo, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "NEEDS_MORE_INFO", afterMoreInfo.ApprovalStatus)

	// Driver resubmits a document — must reopen review (the loop this whole
	// fix closes).
	require.NoError(t, driverSvc.UploadDocument(ctx, u.ID, documents.Selfie, "https://example.com/selfie3.jpg", "", nil))

	afterResubmit, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "PENDING_REVIEW", afterResubmit.ApprovalStatus)

	// Admin can now approve — proves the driver actually resurfaced as
	// actionable, not just that the string changed.
	require.NoError(t, adminSvc.ApproveDriver(ctx, profile.ID, approverID))
	final, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "APPROVED", final.ApprovalStatus)
}

func strPtr(s string) *string { return &s }

func containsDriverID(rows []map[string]interface{}, id string) bool {
	for _, r := range rows {
		if r["id"] == id {
			return true
		}
	}
	return false
}
