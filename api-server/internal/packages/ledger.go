package packages

import (
	"context"
	"errors"

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
//
// Credits are permanent: grants are always written with expires_at = NULL. The
// column remains in the schema (inert) but nothing stamps an expiry — there is
// no longer any sweep that could drain a driver's balance.
func (r *Repository) grant(ctx context.Context, profileID string, vehicleID *string, vehicleTypeID, entryType string, rides, bonus int, sourcePurchaseID *string, adminID *string, reason string, idemKey *string) error {
	tx, hasTx := r.getTx(ctx)
	var err error
	if !hasTx {
		tx, err = r.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
	}

	var curRides, curBonus int
	err = tx.QueryRow(ctx, `
		SELECT rides_remaining, bonus_remaining FROM driver_entitlements
		WHERE driver_id = $1 AND vehicle_type_id = $2 FOR UPDATE
	`, profileID, vehicleTypeID).Scan(&curRides, &curBonus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	newRides, newBonus := curRides+rides, curBonus+bonus

	// ON CONFLICT DO NOTHING makes a repeated call under the same idempotency_key
	// a silent no-op (e.g. a retried admin approval) instead of a 23505 error —
	// exactly-once grant without the caller needing its own dedupe check.
	tag, err := tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_id, vehicle_type_id, entry_type, rides_delta, bonus_delta,
		     balance_rides, balance_bonus, source_purchase_id, admin_id, reason, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, profileID, vehicleID, vehicleTypeID, entryType, rides, bonus,
		newRides, newBonus, sourcePurchaseID, adminID, nullStr(reason), idemKey)
	if err != nil {
		return err
	}
	if idemKey != nil && tag.RowsAffected() == 0 {
		// Already granted under this key — the cache already reflects it from
		// the original call, so there is nothing further to apply.
		return nil
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO driver_entitlements (driver_id, vehicle_id, vehicle_type_id, rides_remaining, bonus_remaining, updated_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (driver_id, vehicle_type_id)
		DO UPDATE SET rides_remaining = $4, bonus_remaining = $5, vehicle_id = COALESCE(EXCLUDED.vehicle_id, driver_entitlements.vehicle_id), updated_at = now()
	`, profileID, vehicleID, vehicleTypeID, newRides, newBonus); err != nil {
		return err
	}
	if !hasTx {
		return tx.Commit(ctx)
	}
	return nil
}

// deductOne removes a single ride (bonus first) and is idempotent on rideID:
// a repeated call for the same ride is a no-op. Returns true if it deducted now.
// deductOne spends one credit for a CITY ride. Its signature and behaviour are
// unchanged and must stay that way — internal/ride/service.go depends on both.
func (r *Repository) deductOne(ctx context.Context, profileID, vehicleTypeID, rideID string) (bool, error) {
	return r.deductOneUnit(ctx, profileID, vehicleTypeID, "RIDE_DEDUCTION", rideID, &rideID, nil)
}

// deductOneUnit is the single implementation behind every credit spend.
//
// It is shared rather than duplicated deliberately: the paid-before-bonus
// ordering below and the post-spend balance snapshot written into the ledger
// are what make balances auditable and reconcilable, and two copies of that
// logic WILL drift — a future policy change (say, bonus-first) would land in one
// and not the other.
//
// sourceRideID and sourceTripID are mutually exclusive; ride_credit_ledger
// carries a CHECK (one_source_only) that enforces it.
//
// NOTE the asymmetry with refundOne, which always credits the PAID bucket
// regardless of which bucket was spent. Do NOT "tidy" these two into a generic
// adjust(+/-1): collapsing them erases that asymmetry and makes an intercity
// refund path trivial to add, which is the one thing the intercity design
// eliminates by construction.
func (r *Repository) deductOneUnit(
	ctx context.Context,
	profileID, vehicleTypeID, entryType, idemKey string,
	sourceRideID, sourceTripID *string,
) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Idempotency is checked BEFORE the balance, and that ordering is
	// load-bearing. The ON CONFLICT below is the race guard, but it is reached
	// only after the balance test — so checking balance first meant a retry of an
	// ALREADY-CHARGED key on a since-emptied balance returned ErrNoCredits
	// ("this driver is broke") instead of (false, nil) ("already paid").
	//
	// That is not cosmetic. The settlement worker treats ErrNoCredits as
	// collectable debt, so a crash between the ledger commit and the charge row
	// being marked CHARGED would leave the driver owing money for a seat they had
	// already paid for — permanently, until they happened to top up.
	var alreadyCharged bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ride_credit_ledger WHERE idempotency_key = $1)`,
		idemKey).Scan(&alreadyCharged); err != nil {
		return false, err
	}
	if alreadyCharged {
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
	if curRides > 0 {
		ridesDelta = -1 // spend paid rides first; bonus is only touched once paid rides are exhausted
		curRides--
	} else {
		bonusDelta = -1
		curBonus--
	}

	// ON CONFLICT rather than a SELECT EXISTS pre-check: two workers that both
	// pass a pre-check would race, and one would get a raw 23505 that reads like
	// a failure when the charge in fact succeeded. RowsAffected() == 0 here means
	// ALREADY CHARGED, which is why this returns (false, nil) and not an error.
	tag, err := tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta,
		     balance_rides, balance_bonus, source_ride_id, source_intercity_trip_id, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (idempotency_key) DO NOTHING
	`, profileID, vehicleTypeID, entryType, ridesDelta, bonusDelta,
		curRides, curBonus, sourceRideID, sourceTripID, idemKey)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already charged
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

// GrantPurchase records a paid package's rides + bonus onto the ledger. Credits
// are permanent (no expires_at); the grant lives until it is spent.
func (l *LedgerService) GrantPurchase(ctx context.Context, profileID string, vehicleID *string, vehicleTypeID, purchaseID string, rides, bonus int) error {
	if rides > 0 {
		if err := l.repo.grant(ctx, profileID, vehicleID, vehicleTypeID, "PURCHASE_GRANT", rides, 0, &purchaseID, nil, "", ptr("grant:"+purchaseID)); err != nil {
			return err
		}
	}
	if bonus > 0 {
		if err := l.repo.grant(ctx, profileID, vehicleID, vehicleTypeID, "BONUS_GRANT", 0, bonus, &purchaseID, nil, "", ptr("bonus:"+purchaseID)); err != nil {
			return err
		}
	}
	return nil
}

// GrantRegistrationBonus mirrors a newly-approved driver's registration bonus
// into the v4 ledger (driver_entitlements, keyed on driver_profiles.id) so it
// is actually spendable at the go-online/accept gates. admin.ApproveDriver
// still writes the legacy bonus_grants row separately (via bonus.Service) for
// the driver-facing bonus history — this is the fix for DB-4: that legacy
// write alone was never read by the spend gates.
//
// Idempotent per driver+vehicle type ("registration:<profileID>" key), so a
// retried approval (e.g. a duplicate admin request) cannot double-grant.
//
// The registration bonus is permanent, like every other credit since the
// credit-expiry removal (#138): it grants with expires_at = NULL and lives
// until it is spent — never swept.
func (l *LedgerService) GrantRegistrationBonus(ctx context.Context, driverUserID, vehicleTypeID string, bonusRides int) error {
	if bonusRides <= 0 {
		return nil
	}
	var profileID string
	err := l.repo.db.QueryRow(ctx, `SELECT id FROM driver_profiles WHERE user_id = $1`, driverUserID).Scan(&profileID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no driver profile — nothing to credit
		}
		return err
	}
	return l.repo.grant(ctx, profileID, nil, vehicleTypeID, "BONUS_GRANT", 0, bonusRides,
		nil, nil, "registration bonus", ptr("registration:"+profileID))
}

// AdminGrant lets a support agent add credits with a reason (audited separately).
func (l *LedgerService) AdminGrant(ctx context.Context, profileID, vehicleTypeID, adminID string, rides, bonus int, reason string) error {
	return l.repo.grant(ctx, profileID, nil, vehicleTypeID, "ADMIN_GRANT", rides, bonus, nil, &adminID, reason, nil)
}

// AdminGrantByCode resolves a vehicle-type code to its id, then grants credits.
func (l *LedgerService) AdminGrantByCode(ctx context.Context, profileID, vehicleTypeCode, adminID string, rides, bonus int, reason string) error {
	var vtID string
	if err := l.repo.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, vehicleTypeCode).Scan(&vtID); err != nil {
		return err
	}
	return l.AdminGrant(ctx, profileID, vtID, adminID, rides, bonus, reason)
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

// GrantFreeTrialIfEligible grants the promotional package's rides to the ledger
// for a newly-approved driver, once per driver (guarded by free_trial_used).
// Mirrors the old behaviour but writes to the v4 ledger.
func (l *LedgerService) GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error {
	var profileID, vehicleTypeID string
	err := l.repo.db.QueryRow(ctx, `SELECT id FROM driver_profiles WHERE user_id = $1`, driverUserID).Scan(&profileID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if err := l.repo.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, vehicleTypeCode).Scan(&vehicleTypeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}

	// Claim the one-time free trial atomically.
	tag, err := l.repo.db.Exec(ctx, `
		UPDATE driver_profiles SET free_trial_used = TRUE, updated_at = now()
		WHERE id = $1 AND free_trial_used = FALSE
	`, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already claimed
	}

	// Find the active promotional version for this vehicle type.
	var rides, bonus int
	err = l.repo.db.QueryRow(ctx, `
		SELECT v.rides, v.bonus_rides
		FROM ride_package_versions v
		JOIN ride_packages p ON p.id = v.package_id
		WHERE p.vehicle_type_id = $1 AND p.is_active = TRUE
		  AND v.status = 'ACTIVE' AND v.is_promotional = TRUE
		ORDER BY v.rides DESC LIMIT 1
	`, vehicleTypeID).Scan(&rides, &bonus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no promo package configured — nothing to grant
		}
		return err
	}
	// Free-trial credits are permanent too (no expires_at).
	return l.repo.grant(ctx, profileID, nil, vehicleTypeID, "BONUS_GRANT", rides, bonus, nil, nil, "free trial", ptr("freetrial:"+profileID))
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

// DeductForIntercity spends one credit for ONE intercity seat.
//
// It takes the driver profile id and vehicle type id DIRECTLY rather than
// resolving them from a user id, because settlement happens after departure:
// re-resolving could land the charge on a different entitlement pool than the
// publish gate checked, if the driver changed or deactivated a vehicle in
// between. The charge row freezes both at departure.
//
// Returns (false, nil) when this key was already charged. Callers MUST treat
// that as SUCCESS — treating it as a failure would bill a driver and then
// penalise them for non-payment.
func (l *LedgerService) DeductForIntercity(ctx context.Context, profileID, vehicleTypeID, idemKey, tripID string) (bool, error) {
	return l.repo.deductOneUnit(ctx, profileID, vehicleTypeID, "INTERCITY_DEDUCTION", idemKey, nil, &tripID)
}
