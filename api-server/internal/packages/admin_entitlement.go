package packages

import (
	"context"
	"time"
)

// AdminEntitlement is the admin-facing view of one driver's balance for one
// vehicle type, enriched with the driver's identity and its ledger history.
// snake_case to match the rest of the admin API; the web console maps it.
type AdminEntitlement struct {
	ID                  string                `json:"id"` // driver_id:vehicle_type_id
	DriverID            string                `json:"driver_id"`
	DriverName          string                `json:"driver_name"`
	DriverPhone         string                `json:"driver_phone"`
	VehicleTypeCode     string                `json:"vehicle_type_code"`
	VehiclePlate        string                `json:"vehicle_plate"`
	RidesRemaining      int                   `json:"rides_remaining"`
	BonusRidesRemaining int                   `json:"bonus_rides_remaining"`
	TotalGranted        int                   `json:"total_granted"`
	TotalConsumed       int                   `json:"total_consumed"`
	Transactions        []AdminEntitlementTxn `json:"transactions"`
}

// AdminEntitlementTxn is one ride_credit_ledger row for the entitlement's history.
type AdminEntitlementTxn struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"` // normalized to the console's kinds
	RidesDelta      int       `json:"rides_delta"`
	BonusRidesDelta int       `json:"bonus_rides_delta"`
	RidesAfter      int       `json:"rides_after"`
	BonusRidesAfter int       `json:"bonus_rides_after"`
	SourceRef       string    `json:"source_ref"`
	Reason          *string   `json:"reason,omitempty"`
	PerformedBy     *string   `json:"performed_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// mapEntryKind normalizes a ledger entry_type to the console's transaction kind.
func mapEntryKind(entryType string) string {
	switch entryType {
	case "ADMIN_GRANT":
		return "admin-grant"
	case "ADMIN_REVOKE", "EXPIRY":
		return "admin-revoke"
	case "RIDE_DEDUCTION":
		return "ride-deduction"
	default: // PURCHASE_GRANT, BONUS_GRANT, RIDE_REFUND
		return "purchase-grant"
	}
}

// ListAllEntitlements returns every driver's per-vehicle-type entitlement with
// lifetime granted/consumed totals and recent ledger transactions. Two queries:
// the balances (with aggregated totals) and the ledger rows, assembled in Go.
func (r *Repository) ListAllEntitlements(ctx context.Context) ([]*AdminEntitlement, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.driver_id, e.vehicle_type_id, vt.code,
		       COALESCE(u.full_name, ''), COALESCE(u.phone_number, ''),
		       COALESCE(dp.vehicle_plate, ''),
		       e.rides_remaining, e.bonus_remaining,
		       COALESCE(agg.total_granted, 0), COALESCE(agg.total_consumed, 0)
		FROM driver_entitlements e
		JOIN driver_profiles dp ON dp.id = e.driver_id
		JOIN users u ON u.id = dp.user_id
		JOIN vehicle_types vt ON vt.id = e.vehicle_type_id
		LEFT JOIN (
			SELECT driver_id, vehicle_type_id,
			       COALESCE(SUM(rides_delta + bonus_delta) FILTER (WHERE rides_delta + bonus_delta > 0), 0)  AS total_granted,
			       COALESCE(-SUM(rides_delta + bonus_delta) FILTER (WHERE rides_delta + bonus_delta < 0), 0) AS total_consumed
			FROM ride_credit_ledger
			GROUP BY driver_id, vehicle_type_id
		) agg ON agg.driver_id = e.driver_id AND agg.vehicle_type_id = e.vehicle_type_id
		ORDER BY u.full_name, vt.code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*AdminEntitlement{}
	// index by driver_id + vehicle_type_id so we can attach transactions.
	byKey := map[string]*AdminEntitlement{}
	for rows.Next() {
		var driverID, vehicleTypeID string
		e := &AdminEntitlement{Transactions: []AdminEntitlementTxn{}}
		if err := rows.Scan(
			&driverID, &vehicleTypeID, &e.VehicleTypeCode,
			&e.DriverName, &e.DriverPhone, &e.VehiclePlate,
			&e.RidesRemaining, &e.BonusRidesRemaining,
			&e.TotalGranted, &e.TotalConsumed,
		); err != nil {
			return nil, err
		}
		e.DriverID = driverID
		e.ID = driverID + ":" + vehicleTypeID
		out = append(out, e)
		byKey[driverID+":"+vehicleTypeID] = e
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// Attach recent ledger transactions (newest first, capped globally).
	txRows, err := r.db.Query(ctx, `
		SELECT id, driver_id, vehicle_type_id, entry_type, rides_delta, bonus_delta,
		       balance_rides, balance_bonus,
		       COALESCE(source_purchase_id::text, source_ride_id::text, admin_id::text, ''),
		       reason, admin_id::text, created_at
		FROM ride_credit_ledger
		ORDER BY created_at DESC
		LIMIT 5000
	`)
	if err != nil {
		return nil, err
	}
	defer txRows.Close()
	for txRows.Next() {
		var driverID, vehicleTypeID, entryType string
		var t AdminEntitlementTxn
		var reason, adminID *string
		if err := txRows.Scan(
			&t.ID, &driverID, &vehicleTypeID, &entryType, &t.RidesDelta, &t.BonusRidesDelta,
			&t.RidesAfter, &t.BonusRidesAfter, &t.SourceRef, &reason, &adminID, &t.CreatedAt,
		); err != nil {
			return nil, err
		}
		ent, ok := byKey[driverID+":"+vehicleTypeID]
		if !ok {
			continue // ledger row for a vehicle type with no live entitlement
		}
		t.Kind = mapEntryKind(entryType)
		t.Reason = reason
		t.PerformedBy = adminID
		ent.Transactions = append(ent.Transactions, t)
	}
	return out, txRows.Err()
}
