package packagepayments

import "time"

// AuditEntry is one row of a claim's audit log.
type AuditEntry struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	ActorType  string    `json:"actor_type"` // driver | admin | system
	ActorID    *string   `json:"actor_id"`
	Action     string    `json:"action"`
	ReasonCode *string   `json:"reason_code"`
}

// Claim mirrors the mobile ClaimDto (snake_case) in
// services/packagePaymentClaims.ts.
type Claim struct {
	ID                    string       `json:"id"`
	Version               int          `json:"version"`
	DriverID              string       `json:"driver_id"`
	VehicleID             string       `json:"vehicle_id"`
	VehicleType           string       `json:"vehicle_type"`
	OfferID               string       `json:"offer_id"`
	PackageID             string       `json:"package_id"`
	PackageVersion        string       `json:"package_version"`
	PackageName           string       `json:"package_name"`
	ExpectedAmountRwf     int64        `json:"expected_amount_rwf"`
	Provider              string       `json:"provider"` // mtn | airtel
	MerchantCodeSnapshot  string       `json:"merchant_code_snapshot"`
	PayerPhoneNumber      string       `json:"payer_phone_number"`
	TransactionReference  *string      `json:"transaction_reference"`
	ProofImageID          *string      `json:"proof_image_id"`
	Status                string       `json:"status"`
	CreatedAt             time.Time    `json:"created_at"`
	SubmittedAt           *time.Time   `json:"submitted_at"`
	ExpiresAt             time.Time    `json:"expires_at"`
	ReviewedAt            *time.Time   `json:"reviewed_at"`
	ReviewedBy            *string      `json:"reviewed_by"`
	RejectionReason       *string      `json:"rejection_reason"`
	ClarificationMessage  *string      `json:"clarification_message"`
	SupportNote           *string      `json:"support_note"`
	ActivationID          *string      `json:"activation_id"`
	PurchaseTransactionID *string      `json:"purchase_transaction_id"`
	UpdatedAt             *time.Time   `json:"updated_at"`
	IdempotencyKey        string       `json:"idempotency_key"`
	AuditLog              []AuditEntry `json:"audit_log"`
}

// CreateInput is the POST /manual-claims body.
type CreateInput struct {
	DriverID             string  `json:"driver_id"`
	VehicleID            string  `json:"vehicle_id"`
	VehicleType          string  `json:"vehicle_type"`
	OfferID              string  `json:"offer_id"`
	PackageID            string  `json:"package_id"`
	PackageVersion       string  `json:"package_version"`
	PackageName          string  `json:"package_name"`
	ExpectedAmountRwf    int64   `json:"expected_amount_rwf"`
	Provider             string  `json:"provider"`
	PayerPhoneNumber     string  `json:"payer_phone_number"`
	TransactionReference *string `json:"transaction_reference"`
	ProofImageID         *string `json:"proof_image_id"`
	IdempotencyKey       string  `json:"idempotency_key"`
}

// ActionInput is the body for submit / resubmit / cancel.
type ActionInput struct {
	ClaimID        string  `json:"claim_id"`
	ReasonCode     *string `json:"reason_code"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// ProviderConfig is one manual-payment provider in the configuration response.
type ProviderConfig struct {
	Provider     string `json:"provider"`
	DisplayName  string `json:"display_name"`
	MerchantCode string `json:"merchant_code"`
	USSDTemplate string `json:"ussd_template"`
	Enabled      bool   `json:"enabled"`
}

// ManualConfig is the "manual" block of the configuration response.
type ManualConfig struct {
	Providers                    []ProviderConfig `json:"providers"`
	ClaimExpiresAfterMinutes     int              `json:"claim_expires_after_minutes"`
	TransactionReferenceRequired bool             `json:"transaction_reference_required"`
	ProofImageEnabled            bool             `json:"proof_image_enabled"`
	ProofImageRequired           bool             `json:"proof_image_required"`
}

// Configuration is the GET /configuration response.
type Configuration struct {
	Mode   string        `json:"mode"` // manual | automatic | disabled
	Manual *ManualConfig `json:"manual"`
	// PricePerRideRwf is the owner-set price of one ride credit, keyed by backend
	// vehicle-type code (MOTO_BIKE, CAB_TAXI, HEAVY_FUSO, LIGHT_HILUX, TUK_TUK).
	// It is the single source of truth the mobile reads to preview how many rides
	// a custom top-up amount buys: rides = floor(amount / price_per_ride_rwf[code]).
	PricePerRideRwf map[string]int64 `json:"price_per_ride_rwf"`
	Version         string           `json:"version"`
	UpdatedAt       time.Time        `json:"updated_at"`
}
