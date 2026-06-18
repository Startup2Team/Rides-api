package packages_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/packages"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Mock repo ─────────────────────────────────────────────────────────────────

type mockRepo struct {
	activeCredit    *packages.DriverCredit
	activeCreditErr error
	deductErr       error
	pkgByID         *packages.Package
	pkgByIDErr      error
	purchasedCredit *packages.DriverCredit
	purchaseErr     error
	grantCalled     int
	grantErr        error
}

func (m *mockRepo) ListPackages(_ context.Context, _ string) ([]*packages.Package, error) {
	return nil, nil
}
func (m *mockRepo) GetPackageByID(_ context.Context, _ string) (*packages.Package, error) {
	return m.pkgByID, m.pkgByIDErr
}
func (m *mockRepo) GetActiveCredit(_ context.Context, _ string) (*packages.DriverCredit, error) {
	return m.activeCredit, m.activeCreditErr
}
func (m *mockRepo) DeductCredit(_ context.Context, _ string) error {
	return m.deductErr
}
func (m *mockRepo) PurchasePackage(_ context.Context, _, _, _ string, _, _ int, _ bool) (*packages.DriverCredit, error) {
	return m.purchasedCredit, m.purchaseErr
}
func (m *mockRepo) GrantFreeTrialIfEligible(_ context.Context, _, _ string) error {
	m.grantCalled++
	return m.grantErr
}
func (m *mockRepo) AdminListPackages(_ context.Context) ([]*packages.Package, error) {
	return nil, nil
}
func (m *mockRepo) AdminCreatePackage(_ context.Context, _, _ string, _, _, _, _ int, _ bool) (*packages.Package, error) {
	return nil, nil
}
func (m *mockRepo) AdminUpdatePackage(_ context.Context, _ string, _ *string, _, _, _, _ *int) (*packages.Package, error) {
	return nil, nil
}
func (m *mockRepo) AdminTogglePackage(_ context.Context, _ string, _ bool) error {
	return nil
}
func (m *mockRepo) AdminDeletePackage(_ context.Context, _ string) error {
	return nil
}

func newSvc(repo packages.Repo) *packages.Service {
	return packages.NewService(repo, zerolog.Nop())
}

// ── HasCredits ────────────────────────────────────────────────────────────────

func TestHasCredits_ReturnsTrue_WhenActiveCreditsExist(t *testing.T) {
	repo := &mockRepo{
		activeCredit: &packages.DriverCredit{
			ID:              "cred-1",
			VehicleTypeCode: "MOTO_BIKE",
			RidesRemaining:  5,
			ExpiresAt:       time.Now().Add(24 * time.Hour),
			IsActive:        true,
		},
	}
	svc := newSvc(repo)

	ok, err := svc.HasCredits(context.Background(), "driver-1", "MOTO_BIKE")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHasCredits_ReturnsFalse_WhenNoCredits(t *testing.T) {
	repo := &mockRepo{activeCreditErr: apperrors.ErrNotFound}
	svc := newSvc(repo)

	ok, err := svc.HasCredits(context.Background(), "driver-1", "MOTO_BIKE")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHasCredits_ReturnsFalse_WhenCreditIsWrongVehicleType(t *testing.T) {
	repo := &mockRepo{
		activeCredit: &packages.DriverCredit{
			VehicleTypeCode: "CAB_TAXI", // driver registered as moto but has cab credits
			RidesRemaining:  10,
			IsActive:        true,
		},
	}
	svc := newSvc(repo)

	ok, err := svc.HasCredits(context.Background(), "driver-1", "MOTO_BIKE")
	require.NoError(t, err)
	assert.False(t, ok, "credits for wrong vehicle type must not count")
}

func TestHasCredits_PropagatesRepoError(t *testing.T) {
	dbErr := errors.New("connection refused")
	repo := &mockRepo{activeCreditErr: dbErr}
	svc := newSvc(repo)

	_, err := svc.HasCredits(context.Background(), "driver-1", "MOTO_BIKE")
	assert.ErrorIs(t, err, dbErr)
}

// ── DeductCredit ──────────────────────────────────────────────────────────────

func TestDeductCredit_CallsRepo_OnSuccess(t *testing.T) {
	repo := &mockRepo{}
	svc := newSvc(repo)

	err := svc.DeductCredit(context.Background(), "driver-1")
	assert.NoError(t, err)
}

func TestDeductCredit_PropagatesRepoError(t *testing.T) {
	repo := &mockRepo{deductErr: errors.New("db error")}
	svc := newSvc(repo)

	err := svc.DeductCredit(context.Background(), "driver-1")
	assert.Error(t, err)
}

// ── BuyPackage ────────────────────────────────────────────────────────────────

func TestBuyPackage_Succeeds_ForValidActivePackage(t *testing.T) {
	want := &packages.DriverCredit{ID: "credit-new", RidesRemaining: 20}
	repo := &mockRepo{
		pkgByID: &packages.Package{
			ID:            "pkg-1",
			VehicleTypeID: "vt-1",
			RideCount:     20,
			ValidityDays:  30,
			IsActive:      true,
			IsPromotional: false,
		},
		purchasedCredit: want,
	}
	svc := newSvc(repo)

	credit, err := svc.BuyPackage(context.Background(), "driver-1", "pkg-1")
	require.NoError(t, err)
	assert.Equal(t, want.ID, credit.ID)
}

func TestBuyPackage_Fails_WhenPackageInactive(t *testing.T) {
	repo := &mockRepo{
		pkgByID: &packages.Package{ID: "pkg-1", IsActive: false},
	}
	svc := newSvc(repo)

	_, err := svc.BuyPackage(context.Background(), "driver-1", "pkg-1")
	require.Error(t, err)
	var ae *apperrors.AppError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "PACKAGE_INACTIVE", ae.Code)
}

func TestBuyPackage_Fails_WhenPackageIsPromotional(t *testing.T) {
	repo := &mockRepo{
		pkgByID: &packages.Package{ID: "pkg-free", IsActive: true, IsPromotional: true},
	}
	svc := newSvc(repo)

	_, err := svc.BuyPackage(context.Background(), "driver-1", "pkg-free")
	require.Error(t, err)
	var ae *apperrors.AppError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "PACKAGE_PROMOTIONAL", ae.Code)
}

func TestBuyPackage_Fails_WhenPackageNotFound(t *testing.T) {
	repo := &mockRepo{pkgByIDErr: apperrors.ErrNotFound}
	svc := newSvc(repo)

	_, err := svc.BuyPackage(context.Background(), "driver-1", "nonexistent")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

// ── GrantFreeTrialIfEligible ──────────────────────────────────────────────────

func TestGrantFreeTrial_CallsRepo(t *testing.T) {
	repo := &mockRepo{}
	svc := newSvc(repo)

	err := svc.GrantFreeTrialIfEligible(context.Background(), "driver-1", "MOTO_BIKE")
	require.NoError(t, err)
	assert.Equal(t, 1, repo.grantCalled)
}

func TestGrantFreeTrial_IsIdempotent_OnSecondCall(t *testing.T) {
	// The repo silently returns nil on the second call (free_trial_used = TRUE).
	repo := &mockRepo{}
	svc := newSvc(repo)

	_ = svc.GrantFreeTrialIfEligible(context.Background(), "driver-1", "MOTO_BIKE")
	err := svc.GrantFreeTrialIfEligible(context.Background(), "driver-1", "MOTO_BIKE")

	require.NoError(t, err)
	assert.Equal(t, 2, repo.grantCalled, "repo called twice but both succeed")
}

func TestGrantFreeTrial_PropagatesRepoError(t *testing.T) {
	repo := &mockRepo{grantErr: errors.New("db error")}
	svc := newSvc(repo)

	err := svc.GrantFreeTrialIfEligible(context.Background(), "driver-1", "MOTO_BIKE")
	assert.Error(t, err)
}

// ── GetCredits ────────────────────────────────────────────────────────────────

func TestGetCredits_ReturnsNil_WhenNoneExist(t *testing.T) {
	repo := &mockRepo{activeCreditErr: apperrors.ErrNotFound}
	svc := newSvc(repo)

	credit, err := svc.GetCredits(context.Background(), "driver-1")
	require.NoError(t, err)
	assert.Nil(t, credit)
}

func TestGetCredits_ReturnsCredit_WhenActive(t *testing.T) {
	want := &packages.DriverCredit{ID: "cred-42", RidesRemaining: 8}
	repo := &mockRepo{activeCredit: want}
	svc := newSvc(repo)

	credit, err := svc.GetCredits(context.Background(), "driver-1")
	require.NoError(t, err)
	assert.Equal(t, want.ID, credit.ID)
	assert.Equal(t, 8, credit.RidesRemaining)
}
