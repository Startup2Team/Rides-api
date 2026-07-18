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
	configVersion            = "v2"
)

// pricePerRideRwf is the owner-set price (RWF) of one ride credit per backend
// vehicle-type code. It is the SINGLE SOURCE OF TRUTH for the custom-amount
// top-up feature: a driver enters an amount and is granted
// floor(amount / pricePerRideRwf[code]) rides on approval. The mobile reads the
// same map from GET /configuration and never hardcodes these numbers.
//
// Owner-provided values: MOTO_BIKE=30, CAB_TAXI=200, HEAVY_FUSO=100,
// TUK_TUK=700 (the "rifani"/"lifani" vehicle — mobile 'rifani' maps to backend
// TUK_TUK). LIGHT_HILUX ('hilux') was left to us to pick a sensible value; 150
// sits between HEAVY_FUSO (100) and CAB_TAXI (200) — CONFIRM with the owner.
var pricePerRideRwf = map[string]int64{
	"MOTO_BIKE":   30,
	"CAB_TAXI":    200,
	"HEAVY_FUSO":  100,
	"LIGHT_HILUX": 150, // chosen by us — pending owner confirmation
	"TUK_TUK":     700, // rifani / "lifani"
}

// vehicleDomainToCode maps the mobile's short vehicle names to backend codes so
// the grant path accepts a claim's vehicle_type in either form.
var vehicleDomainToCode = map[string]string{
	"moto":   "MOTO_BIKE",
	"cab":    "CAB_TAXI",
	"fuso":   "HEAVY_FUSO",
	"hilux":  "LIGHT_HILUX",
	"rifani": "TUK_TUK",
}

// normalizeVehicleCode resolves a claim's vehicle_type (a backend code like
// "MOTO_BIKE" or a mobile domain value like "moto") to the canonical backend
// code used to look up price-per-ride and to grant on the ledger.
func normalizeVehicleCode(v string) string {
	up := strings.ToUpper(strings.TrimSpace(v))
	if _, ok := pricePerRideRwf[up]; ok {
		return up
	}
	if code, ok := vehicleDomainToCode[strings.ToLower(strings.TrimSpace(v))]; ok {
		return code
	}
	return up
}

// isCustomClaim reports whether a claim is a custom-amount top-up (no package).
// Custom claims carry an empty package_id; the ride count is derived from the
// amount and the per-vehicle price-per-ride instead of a fixed package.
func isCustomClaim(c *Claim) bool {
	return strings.TrimSpace(c.PackageID) == ""
}

// ridesForCustomClaim computes floor(expected_amount / pricePerRide[vehicle])
// for a custom-amount claim. Returns false if the vehicle type has no known
// price or the amount buys no rides.
func ridesForCustomClaim(c *Claim) (code string, rides int, ok bool) {
	code = normalizeVehicleCode(c.VehicleType)
	ppr, known := pricePerRideRwf[code]
	if !known || ppr <= 0 || c.ExpectedAmountRwf < ppr {
		return code, 0, false
	}
	return code, int(c.ExpectedAmountRwf / ppr), true
}

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
		PricePerRideRwf: pricePerRideRwf,
		Version:         configVersion,
		UpdatedAt:       s.configuredAt,
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
	if in.VehicleID == "" || in.PayerPhoneNumber == "" || in.ExpectedAmountRwf <= 0 {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_CLAIM", "missing required claim fields")
	}
	// A claim is either a fixed package (package_id set, offer_id set) or a
	// custom-amount top-up (package_id omitted). Custom claims must name a
	// vehicle type with a known price-per-ride so the grant can compute rides.
	custom := strings.TrimSpace(in.PackageID) == ""
	if custom {
		code := normalizeVehicleCode(in.VehicleType)
		if _, ok := pricePerRideRwf[code]; !ok {
			return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_VEHICLE_TYPE", "custom top-up requires a known vehicle type")
		}
		if in.ExpectedAmountRwf < pricePerRideRwf[code] {
			return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_CLAIM", "amount is below the price of one ride")
		}
	} else if in.OfferID == "" {
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
