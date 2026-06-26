package bonus

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Repository handles all bonus DB operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ── Tier management ───────────────────────────────────────────────────────────

func (r *Repository) ListTiers(ctx context.Context, activeOnly bool) ([]*Tier, error) {
	q := `SELECT id, name, COALESCE(description,''), trigger_type, purchase_number,
		         bonus_rides, vehicle_type_id, is_active, created_at, updated_at
		    FROM bonus_tiers`
	if activeOnly {
		q += ` WHERE is_active = TRUE`
	}
	q += ` ORDER BY trigger_type, purchase_number NULLS LAST`

	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tiers []*Tier
	for rows.Next() {
		t := &Tier{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.TriggerType, &t.PurchaseNumber,
			&t.BonusRides, &t.VehicleTypeID, &t.IsActive, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

func (r *Repository) CreateTier(ctx context.Context, in *CreateTierInput) (*Tier, error) {
	t := &Tier{}
	err := r.db.QueryRow(ctx,
		`INSERT INTO bonus_tiers (name, description, trigger_type, purchase_number, bonus_rides, vehicle_type_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, name, COALESCE(description,''), trigger_type, purchase_number,
		           bonus_rides, vehicle_type_id, is_active, created_at, updated_at`,
		in.Name, in.Description, string(in.TriggerType), in.PurchaseNumber, in.BonusRides, in.VehicleTypeID,
	).Scan(&t.ID, &t.Name, &t.Description, &t.TriggerType, &t.PurchaseNumber,
		&t.BonusRides, &t.VehicleTypeID, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (r *Repository) SetTierActive(ctx context.Context, tierID string, active bool) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE bonus_tiers SET is_active = $2, updated_at = NOW() WHERE id = $1`, tierID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// ── Bonus grant logic ─────────────────────────────────────────────────────────

// PurchaseCount returns how many non-promotional paid package purchases
// the driver has made (used to determine which bonus tier fires).
func (r *Repository) PurchaseCount(ctx context.Context, driverID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM driver_ride_credits
		  WHERE driver_id = $1 AND is_promotional = FALSE`, driverID,
	).Scan(&n)
	return n, err
}

// ActiveTierForPurchase returns the best matching active tier for a given
// purchase number. Exact match wins; catch-all (purchase_number IS NULL) is
// the fallback. Returns nil if no tier applies.
func (r *Repository) ActiveTierForPurchase(ctx context.Context, purchaseNumber int) (*Tier, error) {
	t := &Tier{}
	err := r.db.QueryRow(ctx,
		`SELECT id, name, COALESCE(description,''), trigger_type, purchase_number,
		        bonus_rides, vehicle_type_id, is_active, created_at, updated_at
		   FROM bonus_tiers
		  WHERE trigger_type = 'PURCHASE_COUNT'
		    AND is_active = TRUE
		    AND (purchase_number = $1 OR purchase_number IS NULL)
		  ORDER BY purchase_number NULLS LAST   -- exact match first
		  LIMIT 1`,
		purchaseNumber,
	).Scan(&t.ID, &t.Name, &t.Description, &t.TriggerType, &t.PurchaseNumber,
		&t.BonusRides, &t.VehicleTypeID, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // no bonus tier configured for this purchase number
	}
	return t, err
}

// RegistrationTier returns the active REGISTRATION bonus tier, or nil if none.
func (r *Repository) RegistrationTier(ctx context.Context) (*Tier, error) {
	t := &Tier{}
	err := r.db.QueryRow(ctx,
		`SELECT id, name, COALESCE(description,''), trigger_type, purchase_number,
		        bonus_rides, vehicle_type_id, is_active, created_at, updated_at
		   FROM bonus_tiers
		  WHERE trigger_type = 'REGISTRATION' AND is_active = TRUE
		  ORDER BY created_at ASC LIMIT 1`,
	).Scan(&t.ID, &t.Name, &t.Description, &t.TriggerType, &t.PurchaseNumber,
		&t.BonusRides, &t.VehicleTypeID, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// AlreadyGrantedRegistration returns true if the driver already received
// their registration bonus (enforced by unique partial index).
func (r *Repository) AlreadyGrantedRegistration(ctx context.Context, driverID string) (bool, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM bonus_grants bg
		   JOIN bonus_tiers bt ON bt.id = bg.tier_id
		  WHERE bg.driver_id = $1 AND bt.trigger_type = 'REGISTRATION'`,
		driverID,
	).Scan(&n)
	return n > 0, err
}

// InsertGrant records an issued bonus in bonus_grants and inserts the
// corresponding driver_ride_credits row atomically.
func (r *Repository) InsertGrant(ctx context.Context,
	driverID, tierID string,
	triggerCreditID *string,
	vehicleTypeID string,
	bonusRides int,
	expiresAt time.Time,
) (*Grant, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Find a free/promotional package for this vehicle type to reference.
	// If none exists we still proceed — the credit row can have a null package_id
	// by using a sentinel promotional package for the vehicle type.
	// We re-use the free-trial package_id if available (cleaner audit trail).
	var pkgID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM ride_packages
		  WHERE vehicle_type_id = $1 AND is_promotional = TRUE AND is_active = TRUE
		  LIMIT 1`, vehicleTypeID,
	).Scan(&pkgID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No promotional package — use the cheapest active one as reference.
		err = tx.QueryRow(ctx,
			`SELECT id FROM ride_packages
			  WHERE vehicle_type_id = $1 AND is_active = TRUE
			  ORDER BY price_rwf ASC LIMIT 1`, vehicleTypeID,
		).Scan(&pkgID)
		if errors.Is(err, pgx.ErrNoRows) {
			pkgID = "" // no package at all — proceed without reference
		} else if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	// Insert bonus_grants record.
	g := &Grant{}
	scanErr := tx.QueryRow(ctx,
		`INSERT INTO bonus_grants (driver_id, tier_id, trigger_credit_id, vehicle_type_id, bonus_rides, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, driver_id, tier_id, trigger_credit_id, vehicle_type_id, bonus_rides, expires_at, granted_at`,
		driverID, tierID, triggerCreditID, vehicleTypeID, bonusRides, expiresAt,
	).Scan(&g.ID, &g.DriverID, &g.TierID, &g.TriggerCreditID, &g.VehicleTypeID,
		&g.BonusRides, &g.ExpiresAt, &g.GrantedAt)
	if scanErr != nil {
		return nil, scanErr
	}

	// Insert ride credits for the bonus.
	if pkgID != "" {
		_, err = tx.Exec(ctx,
			`INSERT INTO driver_ride_credits
			        (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
			 VALUES ($1, $2, $3, $4, $4, TRUE, $5)`,
			driverID, pkgID, vehicleTypeID, bonusRides, expiresAt,
		)
	} else {
		// No package reference — still grant the credit rows directly.
		_, err = tx.Exec(ctx,
			`INSERT INTO driver_ride_credits
			        (driver_id, package_id, vehicle_type_id, rides_total, rides_remaining, is_promotional, expires_at)
			 SELECT $1, id, $2, $3, $3, TRUE, $4
			   FROM ride_packages WHERE vehicle_type_id = $2 LIMIT 1`,
			driverID, vehicleTypeID, bonusRides, expiresAt,
		)
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Fetch tier name for the response.
	_ = r.db.QueryRow(ctx, `SELECT name FROM bonus_tiers WHERE id = $1`, tierID).Scan(&g.TierName)
	return g, nil
}

// DriverGrants returns the bonus grant history for a driver.
func (r *Repository) DriverGrants(ctx context.Context, driverID string) ([]*Grant, error) {
	rows, err := r.db.Query(ctx,
		`SELECT bg.id, bg.driver_id, bg.tier_id, bt.name,
		        bg.trigger_credit_id, bg.vehicle_type_id,
		        bg.bonus_rides, bg.expires_at, bg.granted_at
		   FROM bonus_grants bg
		   JOIN bonus_tiers bt ON bt.id = bg.tier_id
		  WHERE bg.driver_id = $1
		  ORDER BY bg.granted_at DESC`,
		driverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []*Grant
	for rows.Next() {
		g := &Grant{}
		if err := rows.Scan(&g.ID, &g.DriverID, &g.TierID, &g.TierName,
			&g.TriggerCreditID, &g.VehicleTypeID,
			&g.BonusRides, &g.ExpiresAt, &g.GrantedAt); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
