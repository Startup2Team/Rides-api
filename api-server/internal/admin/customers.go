package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Admin customer management: listing, detail, suspension, bans and
// session revocation.

func (s *Service) ListCustomers(ctx context.Context, status, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	wheres = append(wheres, "u.role_state = 'CUSTOMER_ONLY'")

	if status == "Suspended" {
		wheres = append(wheres, "u.is_suspended = TRUE")
	} else if status == "Active" {
		wheres = append(wheres, "u.is_suspended = FALSE AND u.role_state = 'CUSTOMER_ONLY'")
	}
	if search != "" {
		wheres = append(wheres, fmt.Sprintf("(u.phone_number ILIKE $%d OR u.full_name ILIKE $%d OR u.email ILIKE $%d)", n, n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	base := `FROM users u LEFT JOIN customer_profiles cp ON cp.user_id = u.id`
	where := buildWhere(wheres)

	var total int
	_ = s.db.QueryRow(ctx, "SELECT COUNT(*) "+base+where, args...).Scan(&total)

	orderBy := "u.created_at DESC"
	switch sort {
	case "name":
		orderBy = "u.full_name ASC"
	}

	args = append(args, limit, offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT u.id, u.phone_number, u.email, u.full_name, u.role_state,
		       u.is_suspended, u.suspension_until, u.created_at, u.last_seen_at,
		       COALESCE(cp.rating, 5.0) AS rating,
		       (SELECT COUNT(*) FROM rides WHERE customer_id = u.id AND status = 'COMPLETED') AS total_rides,
		       (SELECT COALESCE(SUM(agreed_fare),0) FROM rides WHERE customer_id = u.id AND status = 'COMPLETED') AS total_spend
		%s %s ORDER BY %s LIMIT $%d OFFSET $%d
	`, base, where, orderBy, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, phone, roleState string
		var email, fullName *string
		var isSuspended bool
		var suspensionUntil, lastSeenAt *time.Time
		var createdAt time.Time
		var rating float64
		var totalRides int
		var totalSpend float64
		if err := rows.Scan(&id, &phone, &email, &fullName, &roleState, &isSuspended,
			&suspensionUntil, &createdAt, &lastSeenAt, &rating, &totalRides, &totalSpend); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "phone": phone, "email": email, "full_name": fullName,
			"role_state": roleState, "is_suspended": isSuspended,
			"suspension_until": suspensionUntil, "created_at": createdAt,
			"last_seen_at": lastSeenAt, "rating": rating,
			"total_rides": totalRides, "total_spend": totalSpend,
		})
	}
	return result, total, nil
}

func (s *Service) CustomerOverview(ctx context.Context) (map[string]interface{}, error) {
	var total, suspended, activeThisWeek int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role_state = 'CUSTOMER_ONLY'`).Scan(&total)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role_state = 'CUSTOMER_ONLY' AND is_suspended = TRUE`).Scan(&suspended)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT customer_id) FROM rides
		WHERE created_at >= NOW() - INTERVAL '7 days'
		  AND customer_id IN (SELECT id FROM users WHERE role_state = 'CUSTOMER_ONLY')
	`).Scan(&activeThisWeek)
	return map[string]interface{}{
		"total":            total,
		"suspended":        suspended,
		"active":           total - suspended,
		"active_this_week": activeThisWeek,
	}, nil
}

func (s *Service) GetCustomer(ctx context.Context, userID string) (map[string]interface{}, error) {
	var id, phone, roleState string
	var email, fullName *string
	var isSuspended bool
	var suspensionUntil, lastSeenAt *time.Time
	var createdAt time.Time
	var rating float64
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.phone_number, u.email, u.full_name, u.role_state,
		       u.is_suspended, u.suspension_until, u.created_at, u.last_seen_at,
		       COALESCE(cp.rating, 5.0) AS rating
		FROM users u
		LEFT JOIN customer_profiles cp ON cp.user_id = u.id
		WHERE u.id = $1
	`, userID).Scan(&id, &phone, &email, &fullName, &roleState, &isSuspended, &suspensionUntil, &createdAt, &lastSeenAt, &rating)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	var totalRides int
	var totalSpend float64
	_ = s.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(agreed_fare),0) FROM rides WHERE customer_id=$1 AND status='COMPLETED'`,
		userID).Scan(&totalRides, &totalSpend)

	rows, _ := s.db.Query(ctx, `
		SELECT r.id, r.status, r.transport_type, r.agreed_fare,
		       r.pickup_address, r.destination_address, r.created_at,
		       r.driver_id, du.full_name AS driver_name, du.phone_number AS driver_phone,
		       dp.vehicle_plate AS vehicle_plate
		FROM rides r
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id
		WHERE r.customer_id = $1
		ORDER BY r.created_at DESC
		LIMIT 10
	`, userID)
	var trips []map[string]interface{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var rID, rStatus, rType, pickupAddr, destAddr string
			var fare *float64
			var rAt time.Time
			var driverID, driverName, driverPhone, vehiclePlate *string
			if err := rows.Scan(&rID, &rStatus, &rType, &fare, &pickupAddr, &destAddr, &rAt, &driverID, &driverName, &driverPhone, &vehiclePlate); err == nil {
				trips = append(trips, map[string]interface{}{
					"id": rID, "status": rStatus, "transport_type": rType,
					"agreed_fare": fare, "pickup_address": pickupAddr,
					"destination_address": destAddr, "created_at": rAt,
					"driver_id":     driverID,
					"driver_name":   driverName,
					"driver_phone":  driverPhone,
					"vehicle_plate": vehiclePlate,
				})
			}
		}
	}

	return map[string]interface{}{
		"id": id, "phone": phone, "email": email, "full_name": fullName,
		"role_state": roleState, "is_suspended": isSuspended,
		"suspension_until": suspensionUntil, "created_at": createdAt,
		"last_seen_at": lastSeenAt, "rating": rating,
		"total_rides": totalRides, "total_spend": totalSpend,
		"recent_trips": trips,
	}, nil
}

func (s *Service) SuspendUser(ctx context.Context, userID, reason string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)
	_, err := s.db.Exec(ctx, `
		UPDATE users SET is_suspended = TRUE, suspension_reason = $1, suspension_until = $2, updated_at = NOW()
		WHERE id = $3
	`, reason, suspendedUntil, userID)
	if err != nil {
		return err
	}
	s.revokeUserSessions(ctx, userID)

	// Send 0-second live push notification & in-app message
	pushTitle := "Account Suspended"
	pushBody := fmt.Sprintf("Your account has been suspended for %d hours.", durationHours)
	if reason != "" {
		pushBody = fmt.Sprintf("Your account has been suspended. Reason: %s", reason)
	}

	if s.notifier != nil {
		s.notifier.SendToAllDevices(ctx, userID, pushTitle, pushBody, "account_suspended", map[string]string{
			"kind":   "account_suspended",
			"reason": reason,
		})
	} else {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO notifications (user_id, title, body, type, data)
			VALUES ($1, $2, $3, 'account_suspended', $4::jsonb)
		`, userID, pushTitle, pushBody, fmt.Sprintf(`{"reason": %q}`, reason))
	}

	return nil
}

func (s *Service) revokeUserSessions(ctx context.Context, userID string) {
	if s.rdb == nil {
		return
	}
	iter := s.rdb.Scan(ctx, 0, "session:"+userID+":*", 100).Iterator()
	for iter.Next(ctx) {
		s.rdb.Del(ctx, iter.Val())
	}
}

func (s *Service) ReinstateUser(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE users SET is_suspended = FALSE, suspension_until = NULL, suspension_reason = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return err
	}

	pushTitle := "Account Reinstated"
	pushBody := "Your account has been reinstated. You can now request rides and use the app normally."
	if s.notifier != nil {
		s.notifier.SendToAllDevices(ctx, userID, pushTitle, pushBody, "account_reinstated", map[string]string{
			"kind": "account_reinstated",
		})
	} else {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO notifications (user_id, title, body, type, data)
			VALUES ($1, $2, $3, 'account_reinstated', '{}'::jsonb)
		`, userID, pushTitle, pushBody)
	}

	return nil
}

func (s *Service) UpdateCustomer(ctx context.Context, userID, status, notes string) error {
	if status != "" {
		_, err := s.db.Exec(ctx,
			`UPDATE users SET role_state = $1, updated_at = NOW() WHERE id = $2`, status, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) BanCustomer(ctx context.Context, userID, reason string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET is_suspended=TRUE, suspension_reason=$1, updated_at=NOW() WHERE id=$2`,
		reason, userID)
	if err != nil {
		return err
	}

	pushTitle := "Account Banned"
	pushBody := "Your account has been permanently suspended."
	if reason != "" {
		pushBody = fmt.Sprintf("Your account has been permanently suspended. Reason: %s", reason)
	}

	if s.notifier != nil {
		s.notifier.SendToAllDevices(ctx, userID, pushTitle, pushBody, "account_banned", map[string]string{
			"kind":   "account_banned",
			"reason": reason,
		})
	} else {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO notifications (user_id, title, body, type, data)
			VALUES ($1, $2, $3, 'account_banned', $4::jsonb)
		`, userID, pushTitle, pushBody, fmt.Sprintf(`{"reason": %q}`, reason))
	}

	return nil
}
