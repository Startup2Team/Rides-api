package packagepayments

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Config carries the manual-payment merchant settings (from the server config)
// needed to build the configuration response and snapshot merchant codes.
type Config struct {
	MTNMerchantCode    string
	AirtelMerchantCode string
}

const (
	claimExpiresAfterMinutes = 120
	configVersion            = "v1"
)

type Service struct {
	repo *Repository
	cfg  Config
	// configuredAt is a stable timestamp so the configuration payload's
	// updated_at doesn't churn on every poll.
	configuredAt time.Time
	// granter grants an approved claim's package rides via the entitlement
	// ledger; notifier persists the driver's in-app review notification. Both
	// are wired in main (SetGranter/SetNotifier) and may be nil in tests.
	granter  PackageGranter
	notifier Notifier
}

func NewService(repo *Repository, cfg Config) *Service {
	return &Service{repo: repo, cfg: cfg, configuredAt: time.Now().UTC()}
}

func (s *Service) providers() []ProviderConfig {
	mtn := s.cfg.MTNMerchantCode
	if mtn == "" {
		mtn = "000000"
	}
	airtel := s.cfg.AirtelMerchantCode
	return []ProviderConfig{
		{
			Provider:     "mtn",
			DisplayName:  "MTN MoMo",
			MerchantCode: mtn,
			USSDTemplate: "*182*8*1*" + mtn + "#",
			Enabled:      true,
		},
		{
			Provider:     "airtel",
			DisplayName:  "Airtel Money",
			MerchantCode: airtel,
			USSDTemplate: "*185*9*" + airtel + "#",
			Enabled:      airtel != "",
		},
	}
}

func (s *Service) merchantCodeFor(provider string) (string, bool) {
	for _, p := range s.providers() {
		if p.Provider == provider {
			if !p.Enabled {
				return "", false
			}
			return p.MerchantCode, true
		}
	}
	return "", false
}

func (s *Service) Configuration() *Configuration {
	return &Configuration{
		Mode: "manual",
		Manual: &ManualConfig{
			Providers:                    s.providers(),
			ClaimExpiresAfterMinutes:     claimExpiresAfterMinutes,
			TransactionReferenceRequired: true,
			ProofImageEnabled:            true,
			ProofImageRequired:           false,
		},
		Version:   configVersion,
		UpdatedAt: s.configuredAt,
	}
}

// hydrate attaches the audit log to a claim.
func (s *Service) hydrate(ctx context.Context, c *Claim) (*Claim, error) {
	log, err := s.repo.AuditLog(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	c.AuditLog = log
	return c, nil
}

func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (*Claim, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Provider != "mtn" && in.Provider != "airtel" {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_PROVIDER", "provider must be mtn or airtel")
	}
	if in.IdempotencyKey == "" {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_CLAIM", "idempotency_key is required")
	}
	if in.VehicleID == "" || in.OfferID == "" || in.PackageID == "" || in.PayerPhoneNumber == "" || in.ExpectedAmountRwf <= 0 {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_CLAIM", "missing required claim fields")
	}
	merchantCode, ok := s.merchantCodeFor(in.Provider)
	if !ok {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "PROVIDER_DISABLED", "selected provider is not enabled")
	}

	driverID := in.DriverID
	if driverID == "" {
		driverID = userID
	}
	now := time.Now().UTC()
	c := &Claim{
		DriverID:             driverID,
		VehicleID:            in.VehicleID,
		VehicleType:          in.VehicleType,
		OfferID:              in.OfferID,
		PackageID:            in.PackageID,
		PackageVersion:       in.PackageVersion,
		PackageName:          in.PackageName,
		ExpectedAmountRwf:    in.ExpectedAmountRwf,
		Provider:             in.Provider,
		MerchantCodeSnapshot: merchantCode,
		PayerPhoneNumber:     in.PayerPhoneNumber,
		TransactionReference: in.TransactionReference,
		ProofImageID:         in.ProofImageID,
		Status:               "created",
		IdempotencyKey:       in.IdempotencyKey,
		ExpiresAt:            now.Add(claimExpiresAfterMinutes * time.Minute),
	}
	created, err := s.repo.Insert(ctx, userID, c)
	if err != nil {
		return nil, err
	}
	// Only log "created" the first time (version 1); an idempotent replay
	// returns the existing claim without a duplicate audit row.
	if created.Version == 1 && len(created.AuditLog) == 0 {
		existingLog, _ := s.repo.AuditLog(ctx, created.ID)
		if len(existingLog) == 0 {
			_ = s.repo.AddAudit(ctx, created.ID, "driver", &userID, "created", nil)
		}
	}
	return s.hydrate(ctx, created)
}

func (s *Service) Get(ctx context.Context, userID, id string) (*Claim, error) {
	_ = s.repo.ExpireStale(ctx, userID)
	c, err := s.repo.FindByID(ctx, userID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return s.hydrate(ctx, c)
}

func (s *Service) List(ctx context.Context, userID string, limit int) ([]*Claim, error) {
	_ = s.repo.ExpireStale(ctx, userID)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	claims, err := s.repo.List(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	for _, c := range claims {
		if _, err := s.hydrate(ctx, c); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

var errClaimConflict = apperrors.New(http.StatusConflict, "CLAIM_STATE_CONFLICT", "claim is not in a state that allows this action")

func (s *Service) transition(ctx context.Context, userID, id string, allowedFrom []string, to, action string, setSubmitted, clearRejection bool, reasonCode *string) (*Claim, error) {
	_ = s.repo.ExpireStale(ctx, userID)
	current, err := s.repo.FindByID(ctx, userID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	ok := false
	for _, st := range allowedFrom {
		if current.Status == st {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errClaimConflict
	}
	updated, err := s.repo.UpdateStatus(ctx, userID, id, to, setSubmitted, clearRejection)
	if err != nil {
		return nil, err
	}
	_ = s.repo.AddAudit(ctx, id, "driver", &userID, action, reasonCode)
	return s.hydrate(ctx, updated)
}

func (s *Service) Submit(ctx context.Context, userID, id string) (*Claim, error) {
	return s.transition(ctx, userID, id, []string{"created"}, "submitted", "submitted", true, false, nil)
}

func (s *Service) Resubmit(ctx context.Context, userID, id string) (*Claim, error) {
	return s.transition(ctx, userID, id, []string{"rejected"}, "submitted", "resubmitted", true, true, nil)
}

func (s *Service) Cancel(ctx context.Context, userID, id string, reasonCode *string) (*Claim, error) {
	return s.transition(ctx, userID, id, []string{"created", "submitted", "rejected"}, "cancelled", "cancelled", false, false, reasonCode)
}
