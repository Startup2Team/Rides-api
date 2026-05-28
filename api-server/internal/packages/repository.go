package packages

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Repository handles all credit and package database operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ListPackages returns active packages for a given vehicle type code.
func (r *Repository) ListPackages(ctx context.Context, vehicleTypeCode string) ([]*Package, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.id, rp.name, rp.vehicle_type_id, vt.code,
		       rp.ride_count, rp.validity_days, rp.price_rwf,
		       rp.is_promotional, rp.is_active, rp.created_at
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE vt.code = $1
		  AND rp.is_active = TRUE
		ORDER BY rp.price_rwf ASC
	`, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pkgs []*Package
	for rows.Next() {
		p := &Package{}
		if err := rows.Scan(
			&p.ID, &p.Name, &p.VehicleTypeID, &p.VehicleTypeCode,
			&p.RideCount, &p.ValidityDays, &p.PriceRWF,
			&p.IsPromotional, &p.IsActive, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, rows.Err()
}

// GetPackageByID returns a single package by its ID.
func (r *Repository) GetPackageByID(ctx context.Context, packageID string) (*Package, error) {
	p := &Package{}
	err := r.db.QueryRow(ctx, `
		SELECT rp.id, rp.name, rp.vehicle_type_id, vt.code,
		       rp.ride_count, rp.validity_days, rp.price_rwf,
		       rp.is_promotional, rp.is_active, rp.created_at
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE rp.id = $1
	`, packageID).Scan(
		&p.ID, &p.Name, &p.VehicleTypeID, &p.VehicleTypeCode,
		&p.RideCount, &p.ValidityDays, &p.PriceRWF,
		&p.IsPromotional, &p.IsActive, &p.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// GetActiveCredit returns the driver's best active credit (promos first, then earliest expiry).
// Returns ErrNotFound if the driver has no usable credits.
func (r *Repository) GetActiveCredit(ctx context.Context, driverUserID string) (*DriverCredit, error) {
	c := &DriverCredit{}
	err := r.db.QueryRow(ctx, `
		SELECT dc.id, dc.driver_id, dc.package_id, dc.vehicle_type_id, vt.code,
		       dc.rides_total, dc.rides_remaining, dc.is_promotional,
		       dc.expires_at, dc.is_active, dc.purchased_at
		FROM driver_ride_credits dc
		JOIN vehicle_types vt ON vt.id = dc.vehicle_type_id
		WHERE dc.driver_id = $1
		  AND dc.is_active = TRUE
		  AND dc.rides_remaining > 0
		  AND dc.expires_at > NOW()
		ORDER BY dc.is_promotional DESC, dc.expires_at ASC
		LIMIT 1
	`, driverUserID).Scan(
		&c.ID, &c.DriverID, &c.PackageID, &c.VehicleTypeID, &c.VehicleTypeCode,
		&c.RidesTotal, &c.RidesRemaining, &c.IsPromotional,
		&c.ExpiresAt, &c.IsActive, &c.PurchasedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// DeductCredit atomically decrements rides_remaining by 1 on the best usable credit.
// It is a no-op if the driver has no usable credit (ride already completed — caller
// should have gated at accept time).
func (r *Repository) DeductCredit(ctx context.Context, driverUserID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_ride_credits
		SET rides_remaining = rides_remaining - 1
		WHERE id = (
		    SELECT id
		    FROM driver_ride_credits
		    WHERE driver_id = $1
		      AND is_active = TRUE
		      AND rides_remaining > 0
		      AND expires_at > NOW()
		    ORDER BY is_promotional DESC, expires_at ASC
		    LIMIT 1
		)
	`, driverUserID)
	return err
}

// PurchasePackage inserts a new credit record for a driver and returns it.
func (r *Repository) PurchasePackage(ctx context.Context, driverUserID, packageID, vehicleTypeID string, ridesTotal, validityDays int, isPromotional bool) (*DriverCredit, error) {
	c := &DriverCredit{}
	err := r.db.QueryRow(ctx, `
		WITH inserted AS (
		    INSERT INTO driver_ride_credits
		      (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
		    VALUES ($1, $2, $3, $4, $4, $5, NOW() + make_interval(days => $6))
		    RETURNING id, driver_id, package_id, vehicle_type_id, rides_total,
		              rides_remaining, is_promotional, expires_at, is_active, purchased_at
		)
		SELECT i.id, i.driver_id, i.package_id, i.vehicle_type_id, vt.code,
		       i.rides_total, i.rides_remaining, i.is_promotional,
		       i.expires_at, i.is_active, i.purchased_at
		FROM inserted i
		JOIN vehicle_types vt ON vt.id = i.vehicle_type_id
	`, driverUserID, packageID, vehicleTypeID, ridesTotal, isPromotional, validityDays).Scan(
		&c.ID, &c.DriverID, &c.PackageID, &c.VehicleTypeID, &c.VehicleTypeCode,
		&c.RidesTotal, &c.RidesRemaining, &c.IsPromotional,
		&c.ExpiresAt, &c.IsActive, &c.PurchasedAt,
	)
	return c, err
}

// GrantFreeTrialIfEligible grants the free-trial package for the driver's vehicle type.
// It is idempotent: if free_trial_used is already TRUE, it silently returns.
// Uses a transaction + SELECT FOR UPDATE to prevent duplicate grants under concurrency.
func (r *Repository) GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Lock the driver profile row to prevent race-condition double-grants.
	var alreadyUsed bool
	err = tx.QueryRow(ctx,
		`SELECT free_trial_used FROM driver_profiles WHERE user_id = $1 FOR UPDATE`,
		driverUserID,
	).Scan(&alreadyUsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // driver profile not found — skip silently
		}
		return err
	}
	if alreadyUsed {
		return nil // already received free trial
	}

	var pkgID, vehicleTypeID string
	var rideCount, validityDays int
	err = tx.QueryRow(ctx, `
		SELECT rp.id, rp.vehicle_type_id, rp.ride_count, rp.validity_days
		FROM ride_packages rp
		JOIN vehicle_types vt ON vt.id = rp.vehicle_type_id
		WHERE vt.code = $1
		  AND rp.is_promotional = TRUE
		  AND rp.price_rwf = 0
		  AND rp.is_active = TRUE
		LIMIT 1
	`, vehicleTypeCode).Scan(&pkgID, &vehicleTypeID, &rideCount, &validityDays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no free trial package configured — skip silently
		}
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO driver_ride_credits
		  (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
		VALUES ($1, $2, $3, $4, $4, TRUE, NOW() + make_interval(days => $5))
	`, driverUserID, pkgID, vehicleTypeID, rideCount, validityDays)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		`UPDATE driver_profiles SET free_trial_used = TRUE WHERE user_id = $1`,
		driverUserID,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
