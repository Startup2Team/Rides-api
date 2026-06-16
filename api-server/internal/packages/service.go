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
	DeductCredit(ctx context.Context, driverUserID string) error
	PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error)
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
	AdminListPackages(ctx context.Context) ([]*Package, error)
	AdminCreatePackage(ctx context.Context, name, vehicleTypeCode string, rideCount, bonusRides, validityDays, priceRWF int, isPromotional bool) (*Package, error)
	AdminUpdatePackage(ctx context.Context, id string, name *string, rideCount, bonusRides, validityDays, priceRWF *int) (*Package, error)
	AdminTogglePackage(ctx context.Context, id string, isActive bool) error
}

// ErrNoCredits is returned when a driver tries to accept a ride with no credits left.
var ErrNoCredits = apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")

// Service handles credit and package business logic.
type Service struct {
	repo Repo
	log  zerolog.Logger
}

func NewService(repo Repo, log zerolog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

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
	// Credit must belong to the driver's vehicle type.
	return credit.VehicleTypeCode == vehicleType, nil
}

// DeductCredit decrements the driver's best usable credit by one.
// Called after a ride is COMPLETED — never for driver or customer cancellations.
func (s *Service) DeductCredit(ctx context.Context, driverUserID string) error {
	return s.repo.DeductCredit(ctx, driverUserID)
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

// BuyPackage validates the package and records the purchase.
// Payment collection (MoMo) is the responsibility of the mobile client — this
// endpoint records a successful purchase that the client confirmed.
func (s *Service) BuyPackage(ctx context.Context, driverUserID, packageID string) (*DriverCredit, error) {
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
		Msg("packages: package purchased")

	return credit, nil
}

// GrantFreeTrialIfEligible issues a free-trial credit for newly approved drivers.
// Safe to call multiple times — the repo ensures only one grant per driver.
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
