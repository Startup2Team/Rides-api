package bonus

import "time"

// TriggerType classifies what causes a bonus to fire.
type TriggerType string

const (
	TriggerPurchaseCount TriggerType = "PURCHASE_COUNT"
	TriggerRegistration  TriggerType = "REGISTRATION"
)

// Tier is an admin-configured bonus rule.
type Tier struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Description    string      `json:"description,omitempty"`
	TriggerType    TriggerType `json:"trigger_type"`
	PurchaseNumber *int        `json:"purchase_number,omitempty"` // nil = catch-all for every purchase beyond last explicit tier
	BonusRides     int         `json:"bonus_rides"`
	VehicleTypeID  *string     `json:"vehicle_type_id,omitempty"` // nil = all vehicle types
	IsActive       bool        `json:"is_active"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// Grant is an immutable record of a bonus that was issued.
type Grant struct {
	ID              string    `json:"id"`
	DriverID        string    `json:"driver_id"`
	TierID          string    `json:"tier_id"`
	TierName        string    `json:"tier_name"`
	TriggerCreditID *string   `json:"trigger_credit_id,omitempty"`
	VehicleTypeID   string    `json:"vehicle_type_id"`
	BonusRides      int       `json:"bonus_rides"`
	ExpiresAt       time.Time `json:"expires_at"`
	GrantedAt       time.Time `json:"granted_at"`
}

// CreateTierInput is the admin request body for creating a bonus tier.
type CreateTierInput struct {
	Name           string      `json:"name"`
	Description    string      `json:"description"`
	TriggerType    TriggerType `json:"trigger_type"`
	PurchaseNumber *int        `json:"purchase_number"`
	BonusRides     int         `json:"bonus_rides"`
	VehicleTypeID  *string     `json:"vehicle_type_id"`
}

// UpdateTierInput contains editable fields.
type UpdateTierInput struct {
	Name           *string `json:"name,omitempty"`
	Description    *string `json:"description,omitempty"`
	PurchaseNumber *int    `json:"purchase_number,omitempty"`
	BonusRides     *int    `json:"bonus_rides,omitempty"`
	IsActive       *bool   `json:"is_active,omitempty"`
}
