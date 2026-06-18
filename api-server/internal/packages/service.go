package packages

import (
	"context"
	"errors"
	"net/http"

	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Repo is the persistence interface consumed by Service. *Repository satisfies it;
// tests can provide a lightweight mock instead.
type Repo interface {
	ListPackages(ctx context.Context, vehicleTypeCode string) ([]*Package, error)
	GetPackageByID(ctx context.Context, packageID string) (*Package, error)
	GetActiveCredit(ctx context.Context, driverUserID string) (*DriverCredit, error)
	SumActiveCredits(ctx context.Context, driverUserID string) (int, error)
	DeductCredit(ctx context.Context, driverUserID string) error
	RefundCredit(ctx context.Context, driverUserID string) error
	PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error)
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
	AdminListPackages(ctx context.Context) ([]*Package, error)
	AdminCreatePackage(ctx context.Context, name, vehicleTypeCode string, rideCount, bonusRides, validityDays, priceRWF int, isPromotional bool) (*Package, error)
	AdminUpdatePackage(ctx context.Context, id string, name *string, rideCount, bonusRides, validityDays, priceRWF *int) (*Package, error)
	AdminTogglePackage(ctx context.Context, id string, isActive bool) error
	AdminDeletePackage(ctx context.Context, id string) error
}

// WalletDeductor is the wallet.Service method subset needed by this package.
// Using an interface avoids an import cycle.
type WalletDeductor interface {
	DeductForPackage(ctx context.Context, userID string, amountRWF int64, packageName string) (interface{}, error)
}

// ErrNoCredits is returned when a driver tries to accept a ride with no credits left.
var ErrNoCredits = apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")



// Service handles credit and package business logic.
type Service struct {
	repo   Repo
	wallet WalletDeductor
	log    zerolog.Logger
}

func NewService(repo Repo, log zerolog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// SetWallet injects the wallet service (called after both are wired in main).
func (s *Service) SetWallet(w WalletDeductor) { s.wallet = w }

// ── Driver-facing ─────────────────────────────────────────────────────────────

// HasCredits returns true if the driver has at least one usable credit for the given
// vehicle type. Called at ride-accept time to gate entry.
func (s *Service) HasCredits(ctx context.Context, driverUserID, vehicleType string) (bool, error) {
	credit, err := s.repo.GetActiveCredit(ctx, driverUserID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return credit.VehicleTypeCode == vehicleType, nil
}

// GetTotalCredits returns the total ride credits remaining across all of the
// driver's active grants — the number shown as "credits left".
func (s *Service) GetTotalCredits(ctx context.Context, driverUserID string) (int, error) {
	return s.repo.SumActiveCredits(ctx, driverUserID)
}

// DeductCredit decrements the driver's best usable credit by one.
// Called when a fare is agreed (NEGOTIATING → CONFIRMED) — the credit is
// committed the moment a deal exists, so going offline mid-ride can't dodge it.
func (s *Service) DeductCredit(ctx context.Context, driverUserID string) error {
	return s.repo.DeductCredit(ctx, driverUserID)
}

// RefundCredit returns one ride to the driver's balance. Only called on
// server-verified blameless cancellations: the customer cancelled after the
// fare was agreed, or a customer no-show (driver geofenced at pickup AND the
// server-timed wait window expired).
func (s *Service) RefundCredit(ctx context.Context, driverUserID string) error {
	return s.repo.RefundCredit(ctx, driverUserID)
}

// ListPackages returns active packages available for purchase for a vehicle type.
func (s *Service) ListPackages(ctx context.Context, vehicleTypeCode string) ([]*Package, error) {
	return s.repo.ListPackages(ctx, vehicleTypeCode)
}

func (s *Service) AdminListPackages(ctx context.Context) ([]*Package, error) {
	return s.repo.AdminListPackages(ctx)
}

func (s *Service) AdminCreatePackage(ctx context.Context, name, vehicleTypeCode string, rideCount, bonusRides, validityDays, priceRWF int, isPromotional bool) (*Package, error) {
	return s.repo.AdminCreatePackage(ctx, name, vehicleTypeCode, rideCount, bonusRides, validityDays, priceRWF, isPromotional)
}

func (s *Service) AdminUpdatePackage(ctx context.Context, id string, name *string, rideCount, bonusRides, validityDays, priceRWF *int) (*Package, error) {
	return s.repo.AdminUpdatePackage(ctx, id, name, rideCount, bonusRides, validityDays, priceRWF)
}

func (s *Service) AdminTogglePackage(ctx context.Context, id string, isActive bool) error {
	return s.repo.AdminTogglePackage(ctx, id, isActive)
}

func (s *Service) GetPackageByID(ctx context.Context, id string) (*Package, error) {
	return s.repo.GetPackageByID(ctx, id)
}

func (s *Service) AdminDeletePackage(ctx context.Context, id string) error {
	return s.repo.AdminDeletePackage(ctx, id)
}

// GetCredits returns the driver's current best active credit, or nil if none.
func (s *Service) GetCredits(ctx context.Context, driverUserID string) (*DriverCredit, error) {
	credit, err := s.repo.GetActiveCredit(ctx, driverUserID)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return credit, nil
}

// BuyPackageFromWallet deducts the package price from the driver's wallet,
// then records the ride-credit grant. Atomic: if wallet deduction fails the
// credit is never created.
func (s *Service) BuyPackageFromWallet(ctx context.Context, driverUserID, packageID string) (*DriverCredit, error) {
	pkg, err := s.repo.GetPackageByID(ctx, packageID)
	if err != nil {
		return nil, err
	}
	if !pkg.IsActive {
		return nil, apperrors.New(http.StatusBadRequest, "PACKAGE_INACTIVE", "this package is no longer available")
	}
	if pkg.IsPromotional {
		return nil, apperrors.New(http.StatusBadRequest, "PACKAGE_PROMOTIONAL", "promotional packages are granted automatically")
	}

	// Deduct from wallet first. If wallet has insufficient funds this returns an error
	// before any credit is created.
	if s.wallet != nil && pkg.PriceRWF > 0 {
		if _, err := s.wallet.DeductForPackage(ctx, driverUserID, int64(pkg.PriceRWF), pkg.Name); err != nil {
			return nil, err
		}
	}

	ridesTotal := pkg.RideCount + pkg.BonusRides
	credit, err := s.repo.PurchasePackage(ctx, driverUserID, packageID, pkg.VehicleTypeID, ridesTotal, pkg.ValidityDays, pkg.IsPromotional)
	if err != nil {
		return nil, err
	}

	s.log.Info().
		Str("driver_id", driverUserID).
		Str("package_id", packageID).
		Str("vehicle_type", pkg.VehicleTypeCode).
		Int("rides", ridesTotal).
		Int("price_rwf", pkg.PriceRWF).
		Msg("packages: package purchased")

	return credit, nil
}

// GrantFreeTrialIfEligible issues a free-trial credit for newly approved drivers.
func (s *Service) GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error {
	if err := s.repo.GrantFreeTrialIfEligible(ctx, driverUserID, vehicleTypeCode); err != nil {
		s.log.Error().Err(err).
			Str("driver_id", driverUserID).
			Str("vehicle_type", vehicleTypeCode).
			Msg("packages: failed to grant free trial")
		return err
	}
	s.log.Info().
		Str("driver_id", driverUserID).
		Str("vehicle_type", vehicleTypeCode).
		Msg("packages: free trial granted")
	return nil
}

