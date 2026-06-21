package packages

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// LedgerService owns the v4 entitlement ledger: every credit change is an
// append-only ride_credit_ledger row, and driver_entitlements is a cache updated
// inside the same transaction. Balances are never edited silently.
//
// It holds *Repository directly (not the Repo interface) so the credit/package
// service mock stays untouched.
type LedgerService struct {
	repo *Repository
	log  zerolog.Logger
}

func NewLedgerService(repo *Repository, log zerolog.Logger) *LedgerService {
	return &LedgerService{repo: repo, log: log}
}

// resolveProfile maps an auth user_id to the driver_profiles.id and resolves the
// vehicle_type_id for a vehicle-type code. vehicleID is the driver's active
// vehicle of that type, if any.
func (r *Repository) resolveProfile(ctx context.Context, userID, vehicleTypeCode string) (profileID, vehicleTypeID string, vehicleID *string, err error) {
	err = r.db.QueryRow(ctx, `SELECT id FROM driver_profiles WHERE user_id = $1`, userID).Scan(&profileID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil, apperrors.ErrNotFound
		}
		return "", "", nil, err
	}
	err = r.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, vehicleTypeCode).Scan(&vehicleTypeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil, apperrors.ErrNotFound
		}
		return "", "", nil, err
	}
	// Best-effort: pick an active vehicle of this type (NULL is acceptable).
	_ = r.db.QueryRow(ctx, `
		SELECT id FROM driver_vehicles
		WHERE driver_id = $1 AND vehicle_type_id = $2 AND is_active = TRUE
		ORDER BY created_at LIMIT 1
	`, profileID, vehicleTypeID).Scan(&vehicleID)
	return profileID, vehicleTypeID, vehicleID, nil
}

// grant inserts a grant ledger entry and bumps the entitlement cache atomically.
// entryType is PURCHASE_GRANT | BONUS_GRANT | ADMIN_GRANT.
func (r *Repository) grant(ctx context.Context, profileID string, vehicleID *string, vehicleTypeID, entryType string, rides, bonus int, sourcePurchaseID *string, expiresAt *time.Time, adminID *string, reason string, idemKey *string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var curRides, curBonus int
	err = tx.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2 FOR UPDATE
	`, profileID, vehicleTypeID).Scan(&curRides, &curBonus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	newRides, newBonus := curRides+rides, curBonus+bonus

	if _, err = tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_id, vehicle_type_id, entry_type, rides_delta, bonus_delta,
		     balance_rides, balance_bonus, source_purchase_id, admin_id, reason, expires_at, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, profileID, vehicleID, vehicleTypeID, entryType, rides, bonus,
		newRides, newBonus, sourcePurchaseID, adminID, nullStr(reason), expiresAt, idemKey); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO driver_entitlements (driver_id, vehicle_id, vehicle_type_id, rides_remaining, bonus_remaining, updated_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (driver_id, vehicle_type_id)
		DO UPDATE SET rides_remaining = $4, bonus_remaining = $5, vehicle_id = COALESCE(EXCLUDED.vehicle_id, driver_entitlements.vehicle_id), updated_at = now()
	`, profileID, vehicleID, vehicleTypeID, newRides, newBonus); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deductOne removes a single ride (bonus first) and is idempotent on rideID:
// a repeated call for the same ride is a no-op. Returns true if it deducted now.
func (r *Repository) deductOne(ctx context.Context, profileID, vehicleTypeID, rideID string) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Idempotency: skip if a deduction for this ride already exists.
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_credit_ledger WHERE idempotency_key = $1)`, rideID).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	var curRides, curBonus int
	err = tx.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2 FOR UPDATE
	`, profileID, vehicleTypeID).Scan(&curRides, &curBonus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNoCredits
		}
		return false, err
	}
	if curRides+curBonus <= 0 {
		return false, ErrNoCredits
	}

	ridesDelta, bonusDelta := 0, 0
	if curBonus > 0 {
		bonusDelta = -1 // spend bonus first
		curBonus--
	} else {
		ridesDelta = -1
		curRides--
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta, balance_rides, balance_bonus, source_ride_id, idempotency_key)
		VALUES ($1,$2,'RIDE_DEDUCTION',$3,$4,$5,$6,$7,$8)
	`, profileID, vehicleTypeID, ridesDelta, bonusDelta, curRides, curBonus, rideID, rideID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE driver_entitlements SET rides_remaining = $3, bonus_remaining = $4, updated_at = now()
		WHERE driver_id = $1 AND vehicle_type_id = $2
	`, profileID, vehicleTypeID, curRides, curBonus); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// refundOne returns a ride (as a regular credit) for a blameless cancellation.
// Idempotent on the refund key derived from rideID.
func (r *Repository) refundOne(ctx context.Context, profileID, vehicleTypeID, rideID string) (bool, error) {
	refundKey := "refund:" + rideID
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_credit_ledger WHERE idempotency_key = $1)`, refundKey).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	var curRides, curBonus int
	err = tx.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2 FOR UPDATE
	`, profileID, vehicleTypeID).Scan(&curRides, &curBonus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	curRides++
	if _, err = tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta, balance_rides, balance_bonus, source_ride_id, idempotency_key)
		VALUES ($1,$2,'RIDE_REFUND',1,0,$3,$4,$5,$6)
	`, profileID, vehicleTypeID, curRides, curBonus, rideID, refundKey); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO driver_entitlements (driver_id, vehicle_type_id, rides_remaining, bonus_remaining, updated_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (driver_id, vehicle_type_id)
		DO UPDATE SET rides_remaining = $3, bonus_remaining = $4, updated_at = now()
	`, profileID, vehicleTypeID, curRides, curBonus); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// listEntitlements returns the cached balances for a driver across vehicle types.
func (r *Repository) listEntitlements(ctx context.Context, profileID string) ([]*Entitlement, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.vehicle_type_id, vt.code, e.rides_remaining, e.bonus_remaining, e.unlimited_until, e.updated_at
		FROM driver_entitlements e
		JOIN vehicle_types vt ON vt.id = e.vehicle_type_id
		WHERE e.driver_id = $1
		ORDER BY vt.code
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entitlement
	for rows.Next() {
		e := &Entitlement{}
		if err := rows.Scan(&e.VehicleTypeID, &e.VehicleTypeCode, &e.RidesRemaining, &e.BonusRemaining, &e.UnlimitedUntil, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.TotalRemaining = e.RidesRemaining + e.BonusRemaining
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ── Service-level API ─────────────────────────────────────────────────────────

// GrantPurchase records a paid package's rides + bonus onto the ledger.
func (l *LedgerService) GrantPurchase(ctx context.Context, profileID string, vehicleID *string, vehicleTypeID, purchaseID string, rides, bonus int, expiresAt time.Time) error {
	if rides > 0 {
		if err := l.repo.grant(ctx, profileID, vehicleID, vehicleTypeID, "PURCHASE_GRANT", rides, 0, &purchaseID, &expiresAt, nil, "", ptr("grant:"+purchaseID)); err != nil {
			return err
		}
	}
	if bonus > 0 {
		if err := l.repo.grant(ctx, profileID, vehicleID, vehicleTypeID, "BONUS_GRANT", 0, bonus, &purchaseID, &expiresAt, nil, "", ptr("bonus:"+purchaseID)); err != nil {
			return err
		}
	}
	return nil
}

// AdminGrant lets a support agent add credits with a reason (audited separately).
func (l *LedgerService) AdminGrant(ctx context.Context, profileID, vehicleTypeID, adminID string, rides, bonus int, reason string) error {
	return l.repo.grant(ctx, profileID, nil, vehicleTypeID, "ADMIN_GRANT", rides, bonus, nil, nil, &adminID, reason, nil)
}

// DeductForRide spends one credit at fare agreement, idempotent on the ride.
func (l *LedgerService) DeductForRide(ctx context.Context, userID, vehicleTypeCode, rideID string) (bool, error) {
	profileID, vehicleTypeID, _, err := l.repo.resolveProfile(ctx, userID, vehicleTypeCode)
	if err != nil {
		return false, err
	}
	return l.repo.deductOne(ctx, profileID, vehicleTypeID, rideID)
}

// RefundForRide returns a credit on a blameless cancellation, idempotent.
func (l *LedgerService) RefundForRide(ctx context.Context, userID, vehicleTypeCode, rideID string) (bool, error) {
	profileID, vehicleTypeID, _, err := l.repo.resolveProfile(ctx, userID, vehicleTypeCode)
	if err != nil {
		return false, err
	}
	return l.repo.refundOne(ctx, profileID, vehicleTypeID, rideID)
}

// HasCredits reports whether the driver has any usable credit for a vehicle type.
func (l *LedgerService) HasCredits(ctx context.Context, userID, vehicleTypeCode string) (bool, error) {
	profileID, vehicleTypeID, _, err := l.repo.resolveProfile(ctx, userID, vehicleTypeCode)
	if err != nil {
		return false, err
	}
	var total int
	err = l.repo.db.QueryRow(ctx, `
		SELECT COALESCE(rides_remaining + bonus_remaining, 0)
		FROM driver_entitlements WHERE driver_id = $1 AND vehicle_type_id = $2
	`, profileID, vehicleTypeID).Scan(&total)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return total > 0, nil
}

// ListEntitlementsForUser returns a driver's balances across vehicle types.
func (l *LedgerService) ListEntitlementsForUser(ctx context.Context, userID string) ([]*Entitlement, error) {
	var profileID string
	err := l.repo.db.QueryRow(ctx, `SELECT id FROM driver_profiles WHERE user_id = $1`, userID).Scan(&profileID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return []*Entitlement{}, nil
		}
		return nil, err
	}
	out, err := l.repo.listEntitlements(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*Entitlement{}
	}
	return out, nil
}

func ptr(s string) *string { return &s }
