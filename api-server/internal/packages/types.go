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
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	VehicleTypeID   string     `json:"vehicle_type_id"`
	VehicleTypeCode string     `json:"vehicle_type_code"`
	RideCount       int        `json:"ride_count"`
	BonusRides      int        `json:"bonus_rides"`
	ValidityDays    int        `json:"validity_days"`
	PriceRWF        int        `json:"price_rwf"`
	IsPromotional   bool       `json:"is_promotional"`
	IsActive        bool       `json:"is_active"`
	CreatedAt       time.Time  `json:"created_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
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
