package packages

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminEntitlementRow is the admin-console view of a driver entitlement balance.
type AdminEntitlementRow struct {
	ID                  string                `json:"id"`
	DriverID            string                `json:"driver_id"`
	DriverUserID        string                `json:"driver_user_id"`
	DriverName          string                `json:"driver_name"`
	DriverPhone         string                `json:"driver_phone"`
	VehicleID           *string               `json:"vehicle_id,omitempty"`
	VehicleType         string                `json:"vehicle_type"`
	VehiclePlate        string                `json:"vehicle_plate"`
	RidesRemaining      int                   `json:"rides_remaining"`
	BonusRidesRemaining int                   `json:"bonus_rides_remaining"`
	TotalGranted        int                   `json:"total_granted"`
	TotalConsumed       int                   `json:"total_consumed"`
	Transactions        []AdminEntitlementTxn `json:"transactions,omitempty"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

// AdminEntitlementTxn is one ledger movement for the admin entitlements UI.
type AdminEntitlementTxn struct {
	ID              string    `json:"id"`
	EntitlementID   string    `json:"entitlement_id"`
	Kind            string    `json:"kind"`
	RidesDelta      int       `json:"rides_delta"`
	BonusRidesDelta int       `json:"bonus_rides_delta"`
	RidesAfter      int       `json:"rides_after"`
	BonusRidesAfter int       `json:"bonus_rides_after"`
	SourceRef       string    `json:"source_ref,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	PerformedBy     string    `json:"performed_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// PackageSubscriber is a driver who purchased a given package.
type PackageSubscriber struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Phone          string     `json:"phone"`
	PurchasedAt    time.Time  `json:"purchased_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RidesRemaining int        `json:"rides_remaining"`
	RidesTotal     int        `json:"rides_total"`
}

func mapLedgerKind(entryType string) string {
	switch entryType {
	case "PURCHASE_GRANT", "BONUS_GRANT":
		return "purchase-grant"
	case "RIDE_DEDUCTION":
		return "ride-deduction"
	case "ADMIN_GRANT":
		return "admin-grant"
	case "ADMIN_REVOKE":
		return "admin-revoke"
	case "RIDE_REFUND":
		return "ride-deduction"
	default:
		return strings.ToLower(entryType)
	}
}

func (r *Repository) listAdminEntitlements(ctx context.Context, includeTxns bool) ([]*AdminEntitlementRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT de.id, de.driver_id, dp.user_id, u.full_name, u.phone_number,
		       de.vehicle_id, vt.code, COALESCE(dp.vehicle_plate, ''),
		       de.rides_remaining, de.bonus_remaining, de.updated_at
		FROM driver_entitlements de
		JOIN driver_profiles dp ON dp.id = de.driver_id
		JOIN users u ON u.id = dp.user_id
		JOIN vehicle_types vt ON vt.id = de.vehicle_type_id
		ORDER BY de.updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AdminEntitlementRow
	for rows.Next() {
		row := &AdminEntitlementRow{}
		if err := rows.Scan(
			&row.ID, &row.DriverID, &row.DriverUserID, &row.DriverName, &row.DriverPhone,
			&row.VehicleID, &row.VehicleType, &row.VehiclePlate,
			&row.RidesRemaining, &row.BonusRidesRemaining, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		granted, consumed, err := r.entitlementTotals(ctx, row.DriverID, row.VehicleType)
		if err != nil {
			return nil, err
		}
		row.TotalGranted = granted
		row.TotalConsumed = consumed
		if includeTxns {
			txns, err := r.listEntitlementLedger(ctx, row.ID, row.DriverID, row.VehicleType)
			if err != nil {
				return nil, err
			}
			row.Transactions = txns
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *Repository) entitlementTotals(ctx context.Context, driverID, vehicleTypeCode string) (granted, consumed int, err error) {
	var vehicleTypeID string
	if err = r.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, vehicleTypeCode).Scan(&vehicleTypeID); err != nil {
		return 0, 0, err
	}
	err = r.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(GREATEST(rides_delta, 0) + GREATEST(bonus_delta, 0)), 0),
			COALESCE(SUM(ABS(LEAST(rides_delta, 0)) + ABS(LEAST(bonus_delta, 0))), 0)
		FROM ride_credit_ledger
		WHERE driver_id = $1 AND vehicle_type_id = $2
	`, driverID, vehicleTypeID).Scan(&granted, &consumed)
	return granted, consumed, err
}

func (r *Repository) listEntitlementLedger(ctx context.Context, entitlementID, driverID, vehicleTypeCode string) ([]AdminEntitlementTxn, error) {
	var vehicleTypeID string
	if err := r.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, vehicleTypeCode).Scan(&vehicleTypeID); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT l.id, l.entry_type, l.rides_delta, l.bonus_delta, l.balance_rides, l.balance_bonus,
		       COALESCE(l.source_purchase_id::text, l.source_ride_id::text, ''),
		       COALESCE(l.reason, ''),
		       COALESCE(a.email, ''),
		       l.created_at
		FROM ride_credit_ledger l
		LEFT JOIN admin_accounts a ON a.id = l.admin_id
		WHERE l.driver_id = $1 AND l.vehicle_type_id = $2
		ORDER BY l.created_at DESC
		LIMIT 50
	`, driverID, vehicleTypeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []AdminEntitlementTxn
	for rows.Next() {
		var entryType string
		var t AdminEntitlementTxn
		if err := rows.Scan(
			&t.ID, &entryType, &t.RidesDelta, &t.BonusRidesDelta, &t.RidesAfter, &t.BonusRidesAfter,
			&t.SourceRef, &t.Reason, &t.PerformedBy, &t.CreatedAt,
		); err != nil {
			return nil, err
		}
		t.Kind = mapLedgerKind(entryType)
		t.EntitlementID = entitlementID
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

func (r *Repository) AdminListEntitlements(ctx context.Context, includeTxns bool) ([]*AdminEntitlementRow, error) {
	return r.listAdminEntitlements(ctx, includeTxns)
}

func (r *Repository) GetEntitlementKeys(ctx context.Context, entitlementID string) (string, string, string, error) {
	return r.getEntitlementByID(ctx, entitlementID)
}

func (r *Repository) AdminListPackageSubscribers(ctx context.Context, packageID string) ([]*PackageSubscriber, error) {
	return r.listPackageSubscribers(ctx, packageID)
}

func (r *Repository) getEntitlementByID(ctx context.Context, entitlementID string) (profileID, vehicleTypeID, vehicleTypeCode string, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT de.driver_id, de.vehicle_type_id, vt.code
		FROM driver_entitlements de
		JOIN vehicle_types vt ON vt.id = de.vehicle_type_id
		WHERE de.id = $1
	`, entitlementID).Scan(&profileID, &vehicleTypeID, &vehicleTypeCode)
	return profileID, vehicleTypeID, vehicleTypeCode, err
}

func (r *Repository) listPackageSubscribers(ctx context.Context, packageID string) ([]*PackageSubscriber, error) {
	rows, err := r.db.Query(ctx, `
		SELECT pp.id, u.full_name, u.phone_number, COALESCE(pp.paid_at, pp.created_at),
		       pp.expires_at,
		       COALESCE(de.rides_remaining + de.bonus_remaining, 0),
		       pp.rides_granted + pp.bonus_rides_granted
		FROM package_purchases pp
		JOIN driver_profiles dp ON dp.id = pp.driver_id
		JOIN users u ON u.id = dp.user_id
		LEFT JOIN driver_entitlements de ON de.driver_id = pp.driver_id AND de.vehicle_type_id = pp.vehicle_type_id
		WHERE pp.package_id = $1 AND pp.status = 'PAID'
		ORDER BY COALESCE(pp.paid_at, pp.created_at) DESC
	`, packageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PackageSubscriber
	for rows.Next() {
		s := &PackageSubscriber{}
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Phone, &s.PurchasedAt, &s.ExpiresAt,
			&s.RidesRemaining, &s.RidesTotal,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdminRevoke deducts credits from an entitlement (clamped at zero).
func (l *LedgerService) AdminRevoke(ctx context.Context, profileID, vehicleTypeID, adminID string, rides, bonus int, reason string) error {
	if rides <= 0 && bonus <= 0 {
		return fmt.Errorf("packages: revoke amount must be positive")
	}
	return l.repo.adminAdjust(ctx, profileID, vehicleTypeID, adminID, -rides, -bonus, reason, "ADMIN_REVOKE")
}

func (r *Repository) adminAdjust(ctx context.Context, profileID, vehicleTypeID, adminID string, ridesDelta, bonusDelta int, reason, entryType string) error {
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
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("packages: entitlement not found")
		}
		return err
	}

	newRides := curRides + ridesDelta
	newBonus := curBonus + bonusDelta
	if newRides < 0 {
		newRides = 0
	}
	if newBonus < 0 {
		newBonus = 0
	}

	actualRidesDelta := newRides - curRides
	actualBonusDelta := newBonus - curBonus
	if actualRidesDelta == 0 && actualBonusDelta == 0 {
		return nil
	}

	if _, err = tx.Exec(ctx, `
		INSERT INTO ride_credit_ledger
		    (driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta, balance_rides, balance_bonus, admin_id, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, profileID, vehicleTypeID, entryType, actualRidesDelta, actualBonusDelta, newRides, newBonus, adminID, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE driver_entitlements SET rides_remaining=$3, bonus_remaining=$4, updated_at=now()
		WHERE driver_id=$1 AND vehicle_type_id=$2
	`, profileID, vehicleTypeID, newRides, newBonus); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
