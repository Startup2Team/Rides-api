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
	ListAllPackages(ctx context.Context) ([]*Package, error)
	GetPackageByID(ctx context.Context, packageID string) (*Package, error)
	CreatePackage(ctx context.Context, p *CreatePackageInput) (*Package, error)
	UpdatePackage(ctx context.Context, packageID string, p *UpdatePackageInput) (*Package, error)
	SetPackageActive(ctx context.Context, packageID string, active bool) error
	GetActiveCredit(ctx context.Context, driverUserID string) (*DriverCredit, error)
	DeductCredit(ctx context.Context, driverUserID string) error
	RefundCredit(ctx context.Context, driverUserID string) error
	PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error)
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
}

// WalletDeductor is the wallet.Service method subset needed by this package.
// Using an interface avoids an import cycle.
type WalletDeductor interface {
	DeductForPackage(ctx context.Context, userID string, amountRWF int64, packageName string) (interface{}, error)
}

// ErrNoCredits is returned when a driver tries to accept a ride with no credits left.
var ErrNoCredits = apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")

// CreatePackageInput is the admin request body for creating a new package.
type CreatePackageInput struct {
	Name            string `json:"name"`
	VehicleTypeCode string `json:"vehicle_type_code"`
	RideCount       int    `json:"ride_count"`
	ValidityDays    int    `json:"validity_days"`
	PriceRWF        int    `json:"price_rwf"`
	CostPerRideRWF  int    `json:"cost_per_ride_rwf"`
	IsPromotional   bool   `json:"is_promotional"`
}

// UpdatePackageInput contains the fields admin can change after creation.
type UpdatePackageInput struct {
	Name           *string `json:"name,omitempty"`
	RideCount      *int    `json:"ride_count,omitempty"`
	ValidityDays   *int    `json:"validity_days,omitempty"`
	PriceRWF       *int    `json:"price_rwf,omitempty"`
	CostPerRideRWF *int    `json:"cost_per_ride_rwf,omitempty"`
	IsPromotional  *bool   `json:"is_promotional,omitempty"`
}

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

	credit, err := s.repo.PurchasePackage(ctx, driverUserID, packageID, pkg.VehicleTypeID, pkg.RideCount, pkg.ValidityDays, false)
	if err != nil {
		return nil, err
	}

	s.log.Info().
		Str("driver_id", driverUserID).
		Str("package_id", packageID).
		Str("vehicle_type", pkg.VehicleTypeCode).
		Int("rides", pkg.RideCount).
		Int("price_rwf", pkg.PriceRWF).
		Msg("packages: purchased from wallet")

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

// ── Admin-facing ──────────────────────────────────────────────────────────────

// AdminListPackages returns all packages (active + inactive) for the admin panel.
func (s *Service) AdminListPackages(ctx context.Context) ([]*Package, error) {
	return s.repo.ListAllPackages(ctx)
}

// AdminCreatePackage creates a new package. Admin sets all pricing.
func (s *Service) AdminCreatePackage(ctx context.Context, input *CreatePackageInput) (*Package, error) {
	if input.Name == "" || input.VehicleTypeCode == "" {
		return nil, apperrors.New(http.StatusBadRequest, "VALIDATION", "name and vehicle_type_code are required")
	}
	if input.RideCount <= 0 {
		return nil, apperrors.New(http.StatusBadRequest, "VALIDATION", "ride_count must be positive")
	}
	if input.PriceRWF < 0 {
		return nil, apperrors.New(http.StatusBadRequest, "VALIDATION", "price_rwf must be >= 0")
	}
	if input.ValidityDays <= 0 {
		input.ValidityDays = 30
	}
	if input.CostPerRideRWF <= 0 {
		input.CostPerRideRWF = 30 // default platform cost per ride
	}
	return s.repo.CreatePackage(ctx, input)
}

// AdminUpdatePackage updates mutable fields of a package.
func (s *Service) AdminUpdatePackage(ctx context.Context, packageID string, input *UpdatePackageInput) (*Package, error) {
	return s.repo.UpdatePackage(ctx, packageID, input)
}

// AdminDeactivatePackage soft-deletes a package (drivers can no longer see or buy it).
func (s *Service) AdminDeactivatePackage(ctx context.Context, packageID string) error {
	return s.repo.SetPackageActive(ctx, packageID, false)
}

// AdminActivatePackage re-enables a previously deactivated package.
func (s *Service) AdminActivatePackage(ctx context.Context, packageID string) error {
	return s.repo.SetPackageActive(ctx, packageID, true)
}
