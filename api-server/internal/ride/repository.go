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

// Ride is the full operational ride record.
type Ride struct {
	ID                  string
	CustomerID          string
	DriverID            *string
	TransportType       string
	Status              Status
	PickupPoint         geo.Point
	PickupAddress       string
	DestinationPoint    geo.Point
	DestinationAddress  string
	EstimatedDistanceKM *float64
	CustomerInitialFare *float64
	AgreedFare          *float64
	FareLockedAt        *time.Time
	CancelReason        *string
	CancelledByRole     *string
	DriverArrivedAt     *time.Time
	StartedAt           *time.Time
	CompletedAt         *time.Time
	PickupExpired       bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
	COALESCE(pickup_expired, FALSE),
	created_at, updated_at
`

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
		&r.PickupExpired, &r.CreatedAt, &r.UpdatedAt,
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
	row := repo.db.QueryRow(ctx, `SELECT `+rideSelectCols+` FROM rides WHERE id = $1 AND customer_id = $2`, rideID, customerID)
	return scanRide(row)
}

func (repo *Repository) FindByIDAndDriver(ctx context.Context, rideID, driverUserID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx, `
		SELECT `+rideSelectCols+`
		FROM rides r
		WHERE r.id = $1
		  AND r.driver_id = (SELECT id FROM driver_profiles WHERE user_id = $2)
	`, rideID, driverUserID)
	return scanRide(row)
}

func (repo *Repository) CreateRide(ctx context.Context, customerID, transportType, pickupAddr, destAddr string, pickup, dest geo.Point, initialFare *float64) (*Ride, error) {
	var id string
	err := repo.db.QueryRow(ctx, `
		INSERT INTO rides (
			customer_id, transport_type, status,
			pickup_point, pickup_address,
			destination_point, destination_address,
			customer_initial_fare
		) VALUES ($1, $2, 'SEARCHING', ST_GeographyFromText($3), $4, ST_GeographyFromText($5), $6, $7)
		RETURNING id
	`, customerID, transportType, pickup.WKT(), pickupAddr, dest.WKT(), destAddr, initialFare).Scan(&id)
	if err != nil {
		return nil, err
	}
	return repo.FindByID(ctx, id)
}

// Transition atomically moves a ride to a new status only if current status matches from.
func (repo *Repository) Transition(ctx context.Context, rideID string, from, to Status) error {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides SET status = $1, updated_at = NOW() WHERE id = $2 AND status = $3
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
		`UPDATE rides SET driver_id = $1, updated_at = NOW() WHERE id = $2`,
		driverProfileID, rideID,
	)
	return err
}

func (repo *Repository) LockFare(ctx context.Context, rideID string, amount float64) error {
	tag, err := repo.db.Exec(ctx, `
		UPDATE rides SET agreed_fare = $1, fare_locked_at = NOW(), updated_at = NOW()
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
		`UPDATE rides SET started_at = NOW(), updated_at = NOW() WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetDriverArrived(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET driver_arrived_at = NOW(), updated_at = NOW() WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetCompleted(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET completed_at = NOW(), updated_at = NOW() WHERE id = $1`, rideID,
	)
	return err
}

func (repo *Repository) SetCompletionDestination(ctx context.Context, rideID string, dest geo.Point, address *string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE rides
		SET destination_point = ST_GeographyFromText($1),
		    destination_address = COALESCE($2, destination_address),
		    updated_at = NOW()
		WHERE id = $3
	`, dest.WKT(), address, rideID)
	return err
}

func (repo *Repository) Cancel(ctx context.Context, rideID, reason, cancelledByRole string) error {
	_, err := repo.db.Exec(ctx, `
		UPDATE rides SET status = 'CANCELLED', cancel_reason = $1, cancelled_by_role = $2, updated_at = NOW()
		WHERE id = $3
	`, reason, cancelledByRole, rideID)
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
		`SELECT `+rideSelectCols+` FROM rides WHERE customer_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		customerID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRides(rows)
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

// FindActiveByCustomer returns the customer's current non-terminal ride.
func (repo *Repository) FindActiveByCustomer(ctx context.Context, customerID string) (*Ride, error) {
	row := repo.db.QueryRow(ctx,
		`SELECT `+rideSelectCols+` FROM rides WHERE customer_id = $1 AND status NOT IN ('COMPLETED','CANCELLED') ORDER BY created_at DESC LIMIT 1`,
		customerID,
	)
	return scanRide(row)
}

// SetPickupExpired marks a ride's pickup window as expired.
func (repo *Repository) SetPickupExpired(ctx context.Context, rideID string) error {
	_, err := repo.db.Exec(ctx,
		`UPDATE rides SET pickup_expired = TRUE, updated_at = NOW() WHERE id = $1`, rideID,
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
