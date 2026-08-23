package waitlist

import "time"

// Canonical role values — mirrors the CUSTOMER/DRIVER split used everywhere
// else in the platform (mw.RoleCustomer / mw.RoleDriverActive etc.).
const (
	RoleCustomer = "CUSTOMER"
	RoleDriver   = "DRIVER"
)

// Signup is a waitlist_signups row.
type Signup struct {
	ID               string     `json:"id"`
	Role             string     `json:"role"`
	Name             string     `json:"name"`
	Phone            string     `json:"phone"`
	Email            *string    `json:"email,omitempty"`
	Area             *string    `json:"area,omitempty"`
	VehicleType      *string    `json:"vehicle_type,omitempty"`
	ReferralCode     string     `json:"referral_code"`
	ReferredBy       *string    `json:"referred_by,omitempty"`
	ConsentLaunch    bool       `json:"consent_launch"`
	ConsentMarketing bool       `json:"consent_marketing"`
	Source           *string    `json:"source,omitempty"`
	OptedOutAt       *time.Time `json:"opted_out_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// CreateInput is the normalized, validated data the repository persists.
type CreateInput struct {
	Role             string
	Name             string
	Phone            string // already normalized to E.164
	Email            *string
	Area             *string
	VehicleType      *string
	ReferredBy       *string
	ConsentLaunch    bool
	ConsentMarketing bool
	Source           *string
}

// ListFilter drives the admin list view.
type ListFilter struct {
	Role   string
	Area   string
	Limit  int
	Offset int
}
