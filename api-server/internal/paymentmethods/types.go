package paymentmethods

import "time"

// Method is a customer-managed payment method (MoMo or cash).
// Wire format is snake_case to match the mobile contract in
// docs/backend/MOBILE_PAYMENT_CONTRACTS.md (Flow F).
type Method struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"` // mtn | airtel | cash
	Label       string    `json:"label"`
	PhoneNumber *string   `json:"phone_number"`
	IsDefault   bool      `json:"is_default"`
	CreatedAt   time.Time `json:"-"`
}

// BillingProfile summarises the caller's billing preferences.
type BillingProfile struct {
	DefaultPaymentMethodID *string  `json:"default_payment_method_id"`
	MobileMoneyMethodIDs   []string `json:"mobile_money_method_ids"`
	CardMethodIDs          []string `json:"card_method_ids"`
	CashEnabled            bool     `json:"cash_enabled"`
}

// AddInput is the POST /methods body.
type AddInput struct {
	Provider       string  `json:"provider"`
	Label          string  `json:"label"`
	PhoneNumber    *string `json:"phone_number"`
	IsDefault      bool    `json:"is_default"`
	IdempotencyKey string  `json:"idempotency_key"`
}

// UpdateInput is the PATCH /methods/{id} body. Pointer fields distinguish
// "absent" from "set to empty/false".
type UpdateInput struct {
	Label          *string `json:"label"`
	PhoneNumber    *string `json:"phone_number"`
	IsDefault      *bool   `json:"is_default"`
	IdempotencyKey string  `json:"idempotency_key"`
}
