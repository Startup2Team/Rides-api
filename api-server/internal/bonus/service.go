package bonus

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// Service handles all bonus logic.
type Service struct {
	repo *Repository
	log  zerolog.Logger
}

func NewService(repo *Repository, log zerolog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// ── Driver-facing ─────────────────────────────────────────────────────────────

// AfterPackagePurchase checks whether this purchase earns a bonus, grants it,
// and returns the grant (or nil if no bonus applies). Called by the packages
// service immediately after a successful BuyPackageFromWallet.
// AfterPackagePurchase implements packages.BonusAfterPurchase.
func (s *Service) AfterPackagePurchase(
	ctx context.Context,
	driverID string,
	creditID string, // the driver_ride_credits.id just created
	vehicleTypeID string,
	creditExpiresAt time.Time,
) (any, error) {
	// Count how many non-promotional purchases the driver has now (includes this one).
	count, err := s.repo.PurchaseCount(ctx, driverID)
	if err != nil {
		s.log.Warn().Err(err).Str("driver_id", driverID).Msg("bonus: failed to get purchase count")
		return nil, nil // non-fatal — skip bonus rather than fail the purchase
	}

	tier, err := s.repo.ActiveTierForPurchase(ctx, count)
	if err != nil {
		s.log.Warn().Err(err).Str("driver_id", driverID).Msg("bonus: tier lookup failed")
		return nil, nil
	}
	if tier == nil {
		return nil, nil // no bonus for this purchase number
	}

	// Bonus expires at same time as the triggering credit (fair usage window).
	expiresAt := creditExpiresAt
	if expiresAt.Before(time.Now().Add(24 * time.Hour)) {
		expiresAt = time.Now().Add(30 * 24 * time.Hour)
	}

	grant, err := s.repo.InsertGrant(ctx, driverID, tier.ID, &creditID, vehicleTypeID, tier.BonusRides, expiresAt)
	if err != nil {
		s.log.Warn().Err(err).Str("driver_id", driverID).Str("tier_id", tier.ID).Msg("bonus: grant insert failed")
		return nil, nil // non-fatal
	}

	s.log.Info().
		Str("driver_id", driverID).
		Str("tier", tier.Name).
		Int("purchase_number", count).
		Int("bonus_rides", tier.BonusRides).
		Msg("bonus: granted purchase bonus")

	return grant, nil
}

// GrantRegistrationBonus grants the REGISTRATION tier bonus to a newly approved
// driver. Idempotent — safe to call multiple times (returns nil if already granted).
// GrantRegistrationBonus implements admin.BonusService.
func (s *Service) GrantRegistrationBonus(ctx context.Context, driverID, vehicleTypeID string) (any, error) {
	already, err := s.repo.AlreadyGrantedRegistration(ctx, driverID)
	if err != nil || already {
		return nil, err // already granted or lookup failed — either way, skip
	}

	tier, err := s.repo.RegistrationTier(ctx)
	if err != nil || tier == nil {
		return nil, err // no registration tier configured
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	grant, err := s.repo.InsertGrant(ctx, driverID, tier.ID, nil, vehicleTypeID, tier.BonusRides, expiresAt)
	if err != nil {
		s.log.Error().Err(err).Str("driver_id", driverID).Msg("bonus: registration grant failed")
		return nil, err
	}

	s.log.Info().
		Str("driver_id", driverID).
		Int("bonus_rides", tier.BonusRides).
		Msg("bonus: registration bonus granted")
	return grant, nil
}

// DriverGrants returns the bonus history for a driver (their own data only).
func (s *Service) DriverGrants(ctx context.Context, driverID string) ([]*Grant, error) {
	return s.repo.DriverGrants(ctx, driverID)
}

// ── Admin-facing ──────────────────────────────────────────────────────────────

// ListTiers returns all tiers (admin sees everything, drivers see only active).
func (s *Service) ListTiers(ctx context.Context, activeOnly bool) ([]*Tier, error) {
	return s.repo.ListTiers(ctx, activeOnly)
}

// CreateTier creates a new admin-defined bonus tier.
func (s *Service) CreateTier(ctx context.Context, in *CreateTierInput) (*Tier, error) {
	if in.BonusRides <= 0 {
		in.BonusRides = 1
	}
	return s.repo.CreateTier(ctx, in)
}

// DeactivateTier disables a bonus tier (drivers stop earning it on future purchases).
func (s *Service) DeactivateTier(ctx context.Context, tierID string) error {
	return s.repo.SetTierActive(ctx, tierID, false)
}

// ActivateTier re-enables a bonus tier.
func (s *Service) ActivateTier(ctx context.Context, tierID string) error {
	return s.repo.SetTierActive(ctx, tierID, true)
}
