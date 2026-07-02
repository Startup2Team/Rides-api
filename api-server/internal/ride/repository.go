package ride

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
)

// Ride is the full operational ride record (internal model — no JSON tags intentionally).
type Ride struct {
	ID         string
	CustomerID string
	// CustomerName and CustomerPhone are populated by driver-side queries (JOIN with users).
	CustomerName  string
	CustomerPhone string
	// DriverName, DriverPhone, DriverRating are populated by customer-side list queries.
	DriverName            string
	DriverPhone           string
	DriverRating          float64
	DriverPlate           string
	DriverID              *string
	TransportType         string
	Status                Status
	PickupPoint           geo.Point
	PickupAddress         string
	DestinationPoint      geo.Point
	DestinationAddress    string
	EstimatedDistanceKM   *float64
	CustomerInitialFare   *float64
	AgreedFare            *float64
	FareLockedAt          *time.Time
	CancelReason          *string
	CancelledByRole       *string
	DriverArrivedAt       *time.Time
	StartedAt             *time.Time
	CompletedAt           *time.Time
	PricingConfigID       *string
	EstimatedFareRWF      *float64
	NightSurchargeApplied bool
	NightSurchargePct     float64
	WaitingSeconds        int
	WaitingChargeRWF      float64
	CancellationFeeRWF    float64
	FinalFareRWF          *float64
	PickupExpired         bool
	RideVersion           int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// RideResponse is the API-facing DTO with snake_case JSON tags expected by the mobile client.
type RideResponse struct {
	ID                    string     `json:"id"`
	CustomerID            string     `json:"customer_id"`
	CustomerName          string     `json:"customer_name,omitempty"`
	CustomerPhone         string     `json:"customer_phone,omitempty"`
	DriverID              *string    `json:"driver_id"`
	DriverName            string     `json:"driver_name,omitempty"`
	DriverPhone           string     `json:"driver_phone,omitempty"`
	DriverRating          *float64   `json:"driver_rating,omitempty"`
	DriverPlate           string     `json:"driver_plate,omitempty"`
	TransportType         string     `json:"transport_type"`
	Status                string     `json:"status"`
	PickupLat             float64    `json:"pickup_lat"`
	PickupLng             float64    `json:"pickup_lng"`
	PickupAddress         string     `json:"pickup_address"`
	DestLat               float64    `json:"dest_lat"`
	DestLng               float64    `json:"dest_lng"`
	DestinationAddress    string     `json:"destination_address"`
	EstimatedDistanceKM   *float64   `json:"estimated_distance_km"`
	CustomerInitialFare   *float64   `json:"customer_initial_fare"`
	AgreedFare            *float64   `json:"agreed_fare"`
	EstimatedFareRWF      *float64   `json:"estimated_fare_rwf"`
	NightSurchargeApplied bool       `json:"night_surcharge_applied"`
	NightSurchargePct     float64    `json:"night_surcharge_pct"`
	WaitingSeconds        int        `json:"waiting_seconds"`
	WaitingChargeRWF      float64    `json:"waiting_charge_rwf"`
	CancellationFeeRWF    float64    `json:"cancellation_fee_rwf"`
	FinalFareRWF          *float64   `json:"final_fare_rwf"`
	CancelReason          *string    `json:"cancel_reason"`
	DriverArrivedAt       *time.Time `json:"driver_arrived_at"`
	StartedAt             *time.Time `json:"started_at"`
	CompletedAt           *time.Time `json:"completed_at"`
	PickupExpired         bool       `json:"pickup_expired"`
	RideVersion           int        `json:"ride_version"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type driverInfo struct {
	Name   string
	Phone  string
	Rating *float64
	Plate  string
}

func (r *Ride) driverInfoOrEmpty() driverInfo {
	if r.DriverID == nil {
		return driverInfo{}
	}
	rating := r.DriverRating
	return driverInfo{
		Name:   r.DriverName,
		Phone:  r.DriverPhone,
		Rating: &rating,
		Plate:  r.DriverPlate,
	}
}

// ToResponse converts the internal Ride model to the mobile-friendly API response.
func (r *Ride) ToResponse() *RideResponse {
	return &RideResponse{
		ID:                    r.ID,
		CustomerID:            r.CustomerID,
		CustomerName:          r.CustomerName,
		CustomerPhone:         r.CustomerPhone,
		DriverID:              r.DriverID,
		DriverName:            r.driverInfoOrEmpty().Name,
		DriverPhone:           r.driverInfoOrEmpty().Phone,
		DriverRating:          r.driverInfoOrEmpty().Rating,
		DriverPlate:           r.driverInfoOrEmpty().Plate,
		TransportType:         r.TransportType,
		Status:                string(r.Status),
		PickupLat:             r.PickupPoint.Lat,
		PickupLng:             r.PickupPoint.Lng,
		PickupAddress:         r.PickupAddress,
		DestLat:               r.DestinationPoint.Lat,
		DestLng:               r.DestinationPoint.Lng,
		DestinationAddress:    r.DestinationAddress,
		EstimatedDistanceKM:   r.EstimatedDistanceKM,
		CustomerInitialFare:   r.CustomerInitialFare,
		AgreedFare:            r.AgreedFare,
		EstimatedFareRWF:      r.EstimatedFareRWF,
		NightSurchargeApplied: r.NightSurchargeApplied,
		NightSurchargePct:     r.NightSurchargePct,
		WaitingSeconds:        r.WaitingSeconds,
		WaitingChargeRWF:      r.WaitingChargeRWF,
		CancellationFeeRWF:    r.CancellationFeeRWF,
		FinalFareRWF:          r.FinalFareRWF,
		CancelReason:          r.CancelReason,
		DriverArrivedAt:       r.DriverArrivedAt,
		StartedAt:             r.StartedAt,
		CompletedAt:           r.CompletedAt,
		PickupExpired:         r.PickupExpired,
		RideVersion:           r.RideVersion,
		CreatedAt:             r.CreatedAt,
		UpdatedAt:             r.UpdatedAt,
	}
}

type RideRequestPayload struct {
	RideID             string
	TransportType      string
	DistanceKM         float64
	PickupLat          float64
	PickupLng          float64
	PickupAddress      string
	DestinationLat     float64
	DestinationLng     float64
	DestinationAddress string
	SuggestedFare      float64
	CustomerName       string
	CustomerPhone      string
}

// Repository handles all ride DB operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const rideSelectCols = `
	id, customer_id, driver_id, transport_type, status,
	ST_X(pickup_point::geometry)      AS pickup_lng,
	ST_Y(pickup_point::geometry)      AS pickup_lat,
	pickup_address,
	ST_X(destination_point::geometry) AS dest_lng,
	ST_Y(destination_point::geometry) AS dest_lat,
	destination_address,
	estimated_distance_km, customer_initial_fare,
	agreed_fare, fare_locked_at,
	cancel_reason, cancelled_by_role,
	driver_arrived_at, started_at, completed_at,
	pricing_config_id, estimated_fare_rwf,
	COALESCE(night_surcharge_applied, FALSE), COALESCE(night_surcharge_pct, 0.0),
	COALESCE(waiting_seconds, 0), COALESCE(waiting_charge_rwf, 0.0),
	COALESCE(cancellation_fee_rwf, 0.0), final_fare_rwf,
	COALESCE(pickup_expired, FALSE),
	COALESCE(ride_version, 1),
	created_at, updated_at
`

// rideSelectColsWithCustomer extends rideSelectCols with customer identity fields.
// Used by driver-facing queries so the driver sees who they're picking up.
const rideSelectColsWithCustomer = `
	r.id, r.customer_id, r.driver_id, r.transport_type, r.status,
	ST_X(r.pickup_point::geometry)      AS pickup_lng,
	ST_Y(r.pickup_point::geometry)      AS pickup_lat,
	r.pickup_address,
	ST_X(r.destination_point::geometry) AS dest_lng,
	ST_Y(r.destination_point::geometry) AS dest_lat,
	r.destination_address,
	r.estimated_distance_km, r.customer_initial_fare,
	r.agreed_fare, r.fare_locked_at,
	r.cancel_reason, r.cancelled_by_role,
	r.driver_arrived_at, r.started_at, r.completed_at,
	r.pricing_config_id, r.estimated_fare_rwf,
	COALESCE(r.night_surcharge_applied, FALSE), COALESCE(r.night_surcharge_pct, 0.0),
	COALESCE(r.waiting_seconds, 0), COALESCE(r.waiting_charge_rwf, 0.0),
	COALESCE(r.cancellation_fee_rwf, 0.0), r.final_fare_rwf,
	COALESCE(r.pickup_expired, FALSE),
	COALESCE(r.ride_version, 1),
	r.created_at, r.updated_at,
	COALESCE(u.full_name, 'Customer')  AS customer_name,
	COALESCE(u.phone_number, '')       AS customer_phone
`

// rideSelectColsWithDriver extends rideSelectCols with driver identity fields.
// Used by customer-facing queries so ride history shows who drove.
const rideSelectColsWithDriver = `
	r.id, r.customer_id, r.driver_id, r.transport_type, r.status,
	ST_X(r.pickup_point::geometry)      AS pickup_lng,
	ST_Y(r.pickup_point::geometry)      AS pickup_lat,
	r.pickup_address,
	ST_X(r.destination_point::geometry) AS dest_lng,
	ST_Y(r.destination_point::geometry) AS dest_lat,
	r.destination_address,
	r.estimated_distance_km, r.customer_initial_fare,
	r.agreed_fare, r.fare_locked_at,
	r.cancel_reason, r.cancelled_by_role,
	r.driver_arrived_at, r.started_at, r.completed_at,
	r.pricing_config_id, r.estimated_fare_rwf,
	COALESCE(r.night_surcharge_applied, FALSE), COALESCE(r.night_surcharge_pct, 0.0),
	COALESCE(r.waiting_seconds, 0), COALESCE(r.waiting_charge_rwf, 0.0),
	COALESCE(r.cancellation_fee_rwf, 0.0), r.final_fare_rwf,
	COALESCE(r.pickup_expired, FALSE),
	COALESCE(r.ride_version, 1),
	r.created_at, r.updated_at,
	COALESCE(du.full_name, '')         AS driver_name,
	COALESCE(du.phone_number, '')      AS driver_phone,
	COALESCE(dp.rating, 5.0)           AS driver_rating,
	COALESCE(dp.vehicle_plate, '')     AS driver_plate
`

func scanRideWithDriver(row pgx.Row) (*Ride, error) {
	r := &Ride{}
	var pickupLng, pickupLat, destLng, destLat float64
	err := row.Scan(
		&r.ID, &r.CustomerID, &r.DriverID, &r.TransportType, &r.Status,
		&pickupLng, &pickupLat, &r.PickupAddress,
		&destLng, &destLat, &r.DestinationAddress,
		&r.EstimatedDistanceKM, &r.CustomerInitialFare,
		&r.AgreedFare, &r.FareLockedAt,
		&r.CancelReason, &r.CancelledByRole,
		&r.DriverArrivedAt, &r.StartedAt, &r.CompletedAt,
		&r.PricingConfigID, &r.EstimatedFareRWF, &r.NightSurchargeApplied, &r.NightSurchargePct,
		&r.WaitingSeconds, &r.WaitingChargeRWF, &r.CancellationFeeRWF, &r.FinalFareRWF,
		&r.PickupExpired, &r.RideVersion, &r.CreatedAt, &r.UpdatedAt,
		&r.DriverName, &r.DriverPhone, &r.DriverRating, &r.DriverPlate,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrRideNotFound
		}
		return nil, err
	}
	r.PickupPoint = geo.Point{Lat: pickupLat, Lng: pickupLng}
	r.DestinationPoint = geo.Point{Lat: destLat, Lng: destLng}
	return r, nil
}

func scanRidesWithDriver(rows pgx.Rows) ([]*Ride, error) {
	var rides []*Ride
	for rows.Next() {
		r, err := scanRideWithDriver(rows)
		if err != nil {
			return nil, err
		}
		rides = append(rides, r)
	}
	return rides, rows.Err()
}

func scanRideWithCustomer(row pgx.Row) (*Ride, error) {
	r := &Ride{}
	var pickupLng, pickupLat, destLng, destLat float64
	err := row.Scan(
		&r.ID, &r.CustomerID, &r.DriverID, &r.TransportType, &r.Status,
		&pickupLng, &pickupLat, &r.PickupAddress,
		&destLng, &destLat, &r.DestinationAddress,
		&r.EstimatedDistanceKM, &r.CustomerInitialFare,
		&r.AgreedFare, &r.FareLockedAt,
		&r.CancelReason, &r.CancelledByRole,
		&r.DriverArrivedAt, &r.StartedAt, &r.CompletedAt,
		&r.PricingConfigID, &r.EstimatedFareRWF, &r.NightSurchargeApplied, &r.NightSurchargePct,
		&r.WaitingSeconds, &r.WaitingChargeRWF, &r.CancellationFeeRWF, &r.FinalFareRWF,
		&r.PickupExpired, &r.RideVersion, &r.CreatedAt, &r.UpdatedAt,
		&r.CustomerName, &r.CustomerPhone,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrRideNotFound
		}
		return nil, err
	}
	r.PickupPoint = geo.Point{Lat: pickupLat, Lng: pickupLng}
	r.DestinationPoint = geo.Point{Lat: destLat, Lng: destLng}
	return r, nil
}

func scanRide(row pgx.Row) (*Ride, error) {
	r := &Ride{}
	var pickupLng, pickupLat, destLng, destLat float64
	err := row.Scan(
		&r.ID, &r.CustomerID, &r.DriverID, &r.TransportType, &r.Status,
		&pickupLng, &pickupLat, &r.PickupAddress,
		&destLng, &destLat, &r.DestinationAddress,
		&r.EstimatedDistanceKM, &r.CustomerInitialFare,
		&r.AgreedFare, &r.FareLockedAt,
		&r.CancelReason, &r.CancelledByRole,
		&r.DriverArrivedAt, &r.StartedAt, &r.CompletedAt,
		&r.PricingConfigID, &r.EstimatedFareRWF, &r.NightSurchargeApplied, &r.NightSurchargePct,
		&r.WaitingSeconds, &r.WaitingChargeRWF, &r.CancellationFeeRWF, &r.FinalFareRWF,
		&r.PickupExpired, &r.RideVersion, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrRideNotFound
		}
		return nil, err
	}
	r.PickupPoint = geo.Point{Lat: pickupLat, Lng: pickupLng}
	r.DestinationPoint = geo.Point{Lat: destLat, Lng: destLng}
	return r, nil
}

func (repo *Repository) FindByID(ctx context.Context, rideID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx, `SELECT `+rideSelectCols+` FROM rides WHERE id = $1`, rideID)
	return scanRide(row)
}

func (repo *Repository) FindByIDAndCustomer(ctx context.Context, rideID, customerID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx, `
		SELECT `+rideSelectColsWithDriver+`
		FROM rides r
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id
		WHERE r.id = $1 AND r.customer_id = $2
	`, rideID, customerID)
	return scanRideWithDriver(row)
}

func (repo *Repository) FindByIDAndDriver(ctx context.Context, rideID, driverUserID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx, `
		SELECT `+rideSelectColsWithCustomer+`
		FROM rides r
		JOIN users u ON u.id = r.customer_id
		WHERE r.id = $1
		  AND r.driver_id = (SELECT id FROM driver_profiles WHERE user_id = $2)
	`, rideID, driverUserID)
	return scanRideWithCustomer(row)
}

func (repo *Repository) FindActiveByDriver(ctx context.Context, driverUserID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx, `
		SELECT `+rideSelectColsWithCustomer+`
		FROM rides r
		JOIN driver_profiles dp ON dp.id = r.driver_id
		JOIN users u ON u.id = r.customer_id
		WHERE dp.user_id = $1
		  AND r.status NOT IN ('COMPLETED','CANCELLED')
		ORDER BY r.created_at DESC
		LIMIT 1
	`, driverUserID)
	return scanRideWithCustomer(row)
}

func (repo *Repository) GetRideRequestPayload(ctx context.Context, rideID string) (*RideRequestPayload, error) {
	row := repo.db.QueryRow(ctx, `
		SELECT r.id,
		       r.transport_type,
		       COALESCE(r.estimated_distance_km, 0),
		       ST_Y(r.pickup_point::geometry) AS pickup_lat,
		       ST_X(r.pickup_point::geometry) AS pickup_lng,
		       r.pickup_address,
		       ST_Y(r.destination_point::geometry) AS dest_lat,
		       ST_X(r.destination_point::geometry) AS dest_lng,
		       r.destination_address,
		       COALESCE(r.estimated_fare_rwf, r.customer_initial_fare, 0) AS suggested_fare,
		       COALESCE(u.full_name, 'Customer') AS customer_name,
		       COALESCE(u.phone_number, '') AS customer_phone
		FROM rides r
		JOIN users u ON u.id = r.customer_id
		WHERE r.id = $1
	`, rideID)

	payload := &RideRequestPayload{}
	if err := row.Scan(
		&payload.RideID,
		&payload.TransportType,
		&payload.DistanceKM,
		&payload.PickupLat,
		&payload.PickupLng,
		&payload.PickupAddress,
		&payload.DestinationLat,
		&payload.DestinationLng,
		&payload.DestinationAddress,
		&payload.SuggestedFare,
		&payload.CustomerName,
		&payload.CustomerPhone,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrRideNotFound
		}
		return nil, err
	}
	return payload, nil
}

func (repo *Repository) CreateRide(ctx context.Context, customerID, transportType, pickupAddr, destAddr string, pickup, dest geo.Point, initialFare, estimatedFare *float64, pricingConfigID *string) (*Ride, error) {
	var id string
	err := repo.db.QueryRow(ctx, `
		INSERT INTO rides (
			customer_id, transport_type, status,
			pickup_point, pickup_address,
			destination_point, destination_address,
			customer_initial_fare, estimated_fare_rwf, pricing_config_id
		) VALUES ($1, $2, 'SEARCHING', ST_GeographyFromText($3), $4, ST_GeographyFromText($5), $6, $7, $8, $9)
		RETURNING id
	`, customerID, transportType, pickup.WKT(), pickupAddr, dest.WKT(), destAddr, initialFare, estimatedFare, pricingConfigID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return repo.FindByID(ctx, id)
}

// Transition atomically moves a ride to a new status only if current status matches from.
func (repo *Repository) Transition(ctx context.Context, rideID string, from, to Status) error {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides SET status = $1, updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $2 AND status = $3
	`, string(to), rideID, string(from))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrInvalidTransition
	}
	return nil
}

func (repo *Repository) AssignDriver(ctx context.Context, rideID, driverProfileID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET driver_id = $1, updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $2`,
		driverProfileID, rideID,
	)
	return err
}

func (repo *Repository) LockFare(ctx context.Context, rideID string, amount float64) error {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides SET agreed_fare = $1, fare_locked_at = NOW(), updated_at = NOW(), ride_version = ride_version + 1
		WHERE id = $2 AND fare_locked_at IS NULL
	`, amount, rideID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrFareLocked
	}
	return nil
}

func (repo *Repository) SetStarted(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET started_at = NOW(), updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetDriverArrived(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET driver_arrived_at = NOW(), updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetCompleted(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET completed_at = NOW(), updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetCompletionDestination(ctx context.Context, rideID string, dest geo.Point, address *string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE rides
		SET destination_point = ST_GeographyFromText($1),
		    destination_address = COALESCE($2, destination_address),
		    updated_at = NOW(),
		    ride_version = ride_version + 1
		WHERE id = $3
	`, dest.WKT(), address, rideID)
	return err
}

// Cancel marks a ride CANCELLED only if it isn't already terminal. The bool
// reports whether THIS call performed the transition — callers use it to make
// side effects (credit refunds, analytics) exactly-once under concurrent cancels.
func (repo *Repository) Cancel(ctx context.Context, rideID, reason, cancelledByRole string) (bool, error) {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides SET status = 'CANCELLED', cancel_reason = $1, cancelled_by_role = $2, updated_at = NOW(), ride_version = ride_version + 1
		WHERE id = $3 AND status NOT IN ('COMPLETED','CANCELLED')
	`, reason, cancelledByRole, rideID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CancelWithFee is Cancel plus a cancellation fee; same exactly-once bool contract.
func (repo *Repository) CancelWithFee(ctx context.Context, rideID, reason, cancelledByRole string, cancellationFee float64) (bool, error) {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides
		SET status = 'CANCELLED',
		    cancel_reason = $1,
		    cancelled_by_role = $2,
		    cancellation_fee_rwf = $3,
		    updated_at = NOW(),
		    ride_version = ride_version + 1
		WHERE id = $4 AND status NOT IN ('COMPLETED','CANCELLED')
	`, reason, cancelledByRole, cancellationFee, rideID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FindDriverUserIDByProfileID resolves a driver profile id to its user id.
// Used by credit charge/refund paths, which are keyed by user id.
func (repo *Repository) FindDriverUserIDByProfileID(ctx context.Context, profileID string) (string, error) {
	var userID string
	err := repo.db.QueryRow(ctx,
		`SELECT user_id FROM driver_profiles WHERE id = $1`, profileID,
	).Scan(&userID)
	return userID, err
}

func (repo *Repository) SetFinalFare(ctx context.Context, rideID string, finalFare *float64, waitingSeconds int, waitingCharge float64, nightApplied bool, nightPct float64) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE rides
		SET final_fare_rwf = $1,
		    waiting_seconds = $2,
		    waiting_charge_rwf = $3,
		    night_surcharge_applied = $4,
		    night_surcharge_pct = $5,
		    updated_at = NOW(),
		    ride_version = ride_version + 1
		WHERE id = $6
	`, finalFare, waitingSeconds, waitingCharge, nightApplied, nightPct, rideID)
	return err
}

func (repo *Repository) AppendEvent(ctx context.Context, rideID, eventType, actorRole, actorID string, payload map[string]interface{}) error {
	_, err := repo.db.Exec(ctx, `
		INSERT INTO ride_events (ride_id, event_type, actor_role, actor_id, payload)
		VALUES ($1, $2, $3, $4, $5)
	`, rideID, eventType, actorRole, actorID, payload)
	return err
}

func (repo *Repository) ListByCustomer(ctx context.Context, customerID string, limit, offset int) ([]*Ride, error) {
	rows, err := repo.db.Query(ctx,
		`SELECT `+rideSelectColsWithDriver+`
		 FROM rides r
		 LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		 LEFT JOIN users du ON du.id = dp.user_id
		 WHERE r.customer_id = $1 ORDER BY r.created_at DESC LIMIT $2 OFFSET $3`,
		customerID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRidesWithDriver(rows)
}

func scanRides(rows pgx.Rows) ([]*Ride, error) {
	var rides []*Ride
	for rows.Next() {
		r, err := scanRide(rows)
		if err != nil {
			return nil, err
		}
		rides = append(rides, r)
	}
	return rides, rows.Err()
}

// DriverWithinRadius checks PostGIS proximity of the driver's current location to a target point.
func (repo *Repository) DriverWithinRadius(ctx context.Context, driverUserID string, target geo.Point, radiusM int) (bool, error) {
	var within bool
	err := repo.db.QueryRow(ctx, `
		SELECT ST_DWithin(dl.location, ST_GeographyFromText($1), $2)
		FROM driver_locations dl
		JOIN driver_profiles dp ON dp.id = dl.driver_id
		WHERE dp.user_id = $3
	`, target.WKT(), radiusM, driverUserID).Scan(&within)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return within, nil
}

// DriverLastLocation returns the driver's most recent recorded location from
// PostGIS. ok=false if there is no row (driver never sent a location).
func (repo *Repository) DriverLastLocation(ctx context.Context, driverUserID string) (geo.Point, bool, error) {
	var lat, lng float64
	err := repo.db.QueryRow(ctx, `
		SELECT ST_Y(dl.location::geometry), ST_X(dl.location::geometry)
		FROM driver_locations dl
		JOIN driver_profiles dp ON dp.id = dl.driver_id
		WHERE dp.user_id = $1
	`, driverUserID).Scan(&lat, &lng)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return geo.Point{}, false, nil
		}
		return geo.Point{}, false, err
	}
	return geo.Point{Lat: lat, Lng: lng}, true, nil
}

// FindActiveByCustomer returns the customer's current non-terminal ride.
func (repo *Repository) FindActiveByCustomer(ctx context.Context, customerID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx,
		`SELECT `+rideSelectCols+` FROM rides WHERE customer_id = $1 AND status NOT IN ('COMPLETED','CANCELLED') ORDER BY created_at DESC LIMIT 1`,
		customerID,
	)
	return scanRide(row)
}

// StaleRide is the minimal projection the dead-man finalizer needs.
type StaleRide struct {
	ID              string
	CustomerID      string
	DriverProfileID string
	DriverUserID    string
	TransportType   string
	AgreedFare      *float64
}

// FindStaleInProgress returns rides stuck IN_PROGRESS longer than
// olderThanMinutes — trips a driver started but never completed (went offline,
// killed the app, etc.).
func (repo *Repository) FindStaleInProgress(ctx context.Context, olderThanMinutes int) ([]*StaleRide, error) {
	rows, err := repo.db.Query(ctx, `
		SELECT r.id, r.customer_id, r.driver_id, dp.user_id, r.transport_type, r.agreed_fare
		FROM rides r
		JOIN driver_profiles dp ON dp.id = r.driver_id
		WHERE r.status = 'IN_PROGRESS'
		  AND r.started_at IS NOT NULL
		  AND r.started_at < NOW() - make_interval(mins => $1)
	`, olderThanMinutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StaleRide
	for rows.Next() {
		sr := &StaleRide{}
		if err := rows.Scan(&sr.ID, &sr.CustomerID, &sr.DriverProfileID, &sr.DriverUserID, &sr.TransportType, &sr.AgreedFare); err != nil {
			continue
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

// SetPickupExpired marks a ride's pickup window as expired.
func (repo *Repository) SetPickupExpired(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET pickup_expired = TRUE, updated_at = NOW(), ride_version = ride_version + 1 WHERE id = $1`, rideID,
	)
	return err
}

// FindDriverProfileByUserID returns a minimal driver profile struct for Redis state management.
func (repo *Repository) FindDriverProfileByUserID(ctx context.Context, userID string) (*driverProfile, error) {
	p := &driverProfile{}
	err := repo.db.QueryRow(ctx,
		`SELECT id, transport_type FROM driver_profiles WHERE user_id = $1`, userID,
	).Scan(&p.ID, &p.TransportType)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// driverProfile is a minimal internal struct used only for Redis state management.
type driverProfile struct {
	ID            string
	TransportType string
}

// IncrementDriverRides increments total_rides on the driver profile by 1.
func (repo *Repository) IncrementDriverRides(ctx context.Context, driverUserID string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE driver_profiles SET total_rides = total_rides + 1, updated_at = NOW()
		WHERE user_id = $1
	`, driverUserID)
	return err
}

// ── Cancellation penalties ──────────────────────────────────────────────────

// IncrementUserBanCount bumps the lifetime ban counter and returns the new value.
func (repo *Repository) IncrementUserBanCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := repo.db.QueryRow(ctx, `
		UPDATE users SET ban_count = ban_count + 1, updated_at = NOW()
		WHERE id = $1
		RETURNING ban_count
	`, userID).Scan(&n)
	return n, err
}

// BanUserUntil applies a temporary suspension that lifts itself at `until`.
func (repo *Repository) BanUserUntil(ctx context.Context, userID string, until time.Time, reason string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE users SET is_suspended = TRUE, suspension_until = $1, suspension_reason = $2, updated_at = NOW()
		WHERE id = $3
	`, until, reason, userID)
	return err
}

// SuspendUserIndefinitely applies a suspension with no expiry (admin must lift).
func (repo *Repository) SuspendUserIndefinitely(ctx context.Context, userID, reason string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE users SET is_suspended = TRUE, suspension_until = NULL, suspension_reason = $1, updated_at = NOW()
		WHERE id = $2
	`, reason, userID)
	return err
}
