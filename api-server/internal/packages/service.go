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
	ListCatalog(ctx context.Context, vehicleTypeCode string) ([]*CatalogPackage, error)
	ListActiveCampaigns(ctx context.Context, vehicleTypeCode string) ([]*Campaign, error)
	ListAllPackages(ctx context.Context) ([]*Package, error)
	GetPackageByID(ctx context.Context, packageID string) (*Package, error)
	GetActiveCredit(ctx context.Context, driverUserID string) (*DriverCredit, error)
	SumActiveCredits(ctx context.Context, driverUserID string) (int, error)
	DeductCredit(ctx context.Context, driverUserID string) error
	RefundCredit(ctx context.Context, driverUserID string) error
	PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error)
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
	CreatePackage(ctx context.Context, p *CreatePackageInput) (*Package, error)
	UpdatePackage(ctx context.Context, packageID string, p *UpdatePackageInput) (*Package, error)
	SetPackageActive(ctx context.Context, packageID string, active bool) error
	DeletePackage(ctx context.Context, packageID string) error

	ListAllCampaigns(ctx context.Context) ([]*AdminCampaign, error)
	GetCampaignByID(ctx context.Context, id string) (*AdminCampaign, error)
	CreateCampaign(ctx context.Context, creatorAdminID string, input *CreateCampaignInput) (*AdminCampaign, error)
	UpdateCampaign(ctx context.Context, id string, input *UpdateCampaignInput) (*AdminCampaign, error)
	SetCampaignStatus(ctx context.Context, id string, status string) error
	DeleteCampaign(ctx context.Context, id string) error

	AdminListEntitlements(ctx context.Context, includeTxns bool) ([]*AdminEntitlementRow, error)
	GetEntitlementKeys(ctx context.Context, entitlementID string) (profileID, vehicleTypeID, vehicleTypeCode string, err error)
	AdminListPackageSubscribers(ctx context.Context, packageID string) ([]*PackageSubscriber, error)
}

// WalletDeductor is the wallet.Service method subset needed by this package.
// Using an interface avoids an import cycle.
type WalletDeductor interface {
	DeductForPackage(ctx context.Context, userID string, amountRWF int64, packageName string) (interface{}, error)
}

// ErrNoCredits is returned when a driver tries to accept a ride with no credits left.
var ErrNoCredits = apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")

type CreatePackageInput struct {
	Name            string `json:"name" validate:"required"`
	VehicleTypeCode string `json:"vehicle_type_code" validate:"required"`
	RideCount       int    `json:"ride_count" validate:"required,min=1"`
	BonusRides      int    `json:"bonus_rides" validate:"min=0"`
	ValidityDays    int    `json:"validity_days" validate:"required,min=1"`
	PriceRWF        int    `json:"price_rwf" validate:"min=0"`
	CostPerRideRWF  int    `json:"cost_per_ride_rwf" validate:"min=0"`
	IsPromotional   bool   `json:"is_promotional"`
}

type UpdatePackageInput struct {
	Name           *string `json:"name,omitempty"`
	RideCount      *int    `json:"ride_count,omitempty" validate:"omitempty,min=1"`
	BonusRides     *int    `json:"bonus_rides,omitempty" validate:"omitempty,min=0"`
	ValidityDays   *int    `json:"validity_days,omitempty" validate:"omitempty,min=1"`
	PriceRWF       *int    `json:"price_rwf,omitempty" validate:"omitempty,min=0"`
	CostPerRideRWF *int    `json:"cost_per_ride_rwf,omitempty" validate:"omitempty,min=0"`
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
	return s.repo.ListAllPackages(ctx)
}

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

func (s *Service) AdminUpdatePackage(ctx context.Context, id string, input *UpdatePackageInput) (*Package, error) {
	return s.repo.UpdatePackage(ctx, id, input)
}

func (s *Service) AdminTogglePackage(ctx context.Context, id string, isActive bool) error {
	return s.repo.SetPackageActive(ctx, id, isActive)
}

func (s *Service) GetPackageByID(ctx context.Context, id string) (*Package, error) {
	return s.repo.GetPackageByID(ctx, id)
}

func (s *Service) AdminDeletePackage(ctx context.Context, id string) error {
	return s.repo.DeletePackage(ctx, id)
}

func (s *Service) AdminListCampaigns(ctx context.Context) ([]*AdminCampaign, error) {
	return s.repo.ListAllCampaigns(ctx)
}

func (s *Service) AdminCreateCampaign(ctx context.Context, creatorAdminID string, input *CreateCampaignInput) (*AdminCampaign, error) {
	return s.repo.CreateCampaign(ctx, creatorAdminID, input)
}

func (s *Service) AdminUpdateCampaign(ctx context.Context, id string, input *UpdateCampaignInput) (*AdminCampaign, error) {
	return s.repo.UpdateCampaign(ctx, id, input)
}

func (s *Service) AdminSetCampaignStatus(ctx context.Context, id string, status string) error {
	return s.repo.SetCampaignStatus(ctx, id, status)
}

func (s *Service) AdminDeleteCampaign(ctx context.Context, id string) error {
	return s.repo.DeleteCampaign(ctx, id)
}

func (s *Service) AdminListEntitlements(ctx context.Context, includeTxns bool) ([]*AdminEntitlementRow, error) {
	return s.repo.AdminListEntitlements(ctx, includeTxns)
}

func (s *Service) GetEntitlementKeys(ctx context.Context, entitlementID string) (string, string, string, error) {
	return s.repo.GetEntitlementKeys(ctx, entitlementID)
}

func (s *Service) AdminListPackageSubscribers(ctx context.Context, packageID string) ([]*PackageSubscriber, error) {
	return s.repo.AdminListPackageSubscribers(ctx, packageID)
}

func (s *Service) GetCampaignByID(ctx context.Context, id string) (*AdminCampaign, error) {
	return s.repo.GetCampaignByID(ctx, id)
}

// ListCatalog returns the v4 buyable catalog (active version + campaign applied).
func (s *Service) ListCatalog(ctx context.Context, vehicleTypeCode string) ([]*CatalogPackage, error) {
	return s.repo.ListCatalog(ctx, vehicleTypeCode)
}

// ListActiveCampaigns returns currently-running campaigns for a vehicle type.
func (s *Service) ListActiveCampaigns(ctx context.Context, vehicleTypeCode string) ([]*Campaign, error) {
	return s.repo.ListActiveCampaigns(ctx, vehicleTypeCode)
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
