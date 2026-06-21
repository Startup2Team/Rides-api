package packages

import "time"

type VehicleType struct {
	ID            string    `json:"id"`
	Code          string    `json:"code"`
	DisplayName   string    `json:"display_name"`
	BaseFareRWF   int       `json:"base_fare_rwf"`
	PerKmFareRWF  int       `json:"per_km_fare_rwf"`
	MinFareRWF    int       `json:"min_fare_rwf"`
	MaxPassengers int       `json:"max_passengers"`
	CreditCostRWF int       `json:"credit_cost_rwf"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

type Package struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	VehicleTypeID   string    `json:"vehicle_type_id"`
	VehicleTypeCode string    `json:"vehicle_type_code"`
	RideCount       int       `json:"ride_count"`
	ValidityDays    int       `json:"validity_days"`
	PriceRWF        int       `json:"price_rwf"`
	IsPromotional   bool      `json:"is_promotional"`
	IsActive        bool      `json:"is_active"`
	CreatedAt       time.Time `json:"created_at"`
}

// CatalogPackage is the v4 mobile-facing package shape: the active version's
// values with any active campaign override applied. Field names mirror the
// mobile DriverRidePackage model so the app can consume it directly.
type CatalogPackage struct {
	ID              string `json:"id"`   // package uuid (used for purchase)
	Code            string `json:"code"` // stable key e.g. "growth"
	Name            string `json:"name"`
	VehicleTypeCode string `json:"vehicle_type_code"`

	NormalPriceRWF  int  `json:"normal_price_rwf"`  // version base price
	CurrentPriceRWF int  `json:"current_price_rwf"` // after campaign override
	IncludedRides   int  `json:"included_rides"`
	BonusRides      int  `json:"bonus_rides"`
	TotalCredits    int  `json:"total_credits"`
	ValidityDays    int  `json:"validity_days"`
	IsUnlimited     bool `json:"is_unlimited"`
	LaunchOffer     bool `json:"launch_offer"` // promotional / auto-granted

	VersionID     string  `json:"version_id"`
	VersionNumber int     `json:"version_number"`
	CampaignID    *string `json:"campaign_id,omitempty"`
	CampaignCode  *string `json:"campaign_code,omitempty"`

	// Legacy fields kept so the pre-v4 mobile mapping still works.
	PriceRWF      int  `json:"price_rwf"`
	RideCount     int  `json:"ride_count"`
	IsPromotional bool `json:"is_promotional"`
}

// Campaign is the mobile-facing view of an active campaign.
type Campaign struct {
	ID                 string     `json:"id"`
	Code               string     `json:"code"`
	Name               string     `json:"name"`
	Type               string     `json:"type"`
	StartsAt           *time.Time `json:"starts_at,omitempty"`
	EndsAt             *time.Time `json:"ends_at,omitempty"`
	OverridePriceRWF   *int       `json:"override_price_rwf,omitempty"`
	OverrideRides      *int       `json:"override_rides,omitempty"`
	OverrideBonusRides *int       `json:"override_bonus_rides,omitempty"`
}

// Entitlement is a driver's current credit balance for one vehicle type,
// derived from the ride_credit_ledger and cached in driver_entitlements.
type Entitlement struct {
	VehicleTypeID   string     `json:"vehicle_type_id"`
	VehicleTypeCode string     `json:"vehicle_type_code"`
	RidesRemaining  int        `json:"rides_remaining"`
	BonusRemaining  int        `json:"bonus_remaining"`
	TotalRemaining  int        `json:"total_remaining"`
	UnlimitedUntil  *time.Time `json:"unlimited_until,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type DriverCredit struct {
	ID              string    `json:"id"`
	DriverID        string    `json:"driver_id"`
	PackageID       string    `json:"package_id"`
	VehicleTypeID   string    `json:"vehicle_type_id"`
	VehicleTypeCode string    `json:"vehicle_type_code"`
	RidesTotal      int       `json:"rides_total"`
	RidesRemaining  int       `json:"rides_remaining"`
	IsPromotional   bool      `json:"is_promotional"`
	ExpiresAt       time.Time `json:"expires_at"`
	IsActive        bool      `json:"is_active"`
	PurchasedAt     time.Time `json:"purchased_at"`
}
