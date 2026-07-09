package admin

import (
	"context"
	"fmt"
	"time"
)

// Admin negotiation oversight: history, stats and per-ride detail.

func (s *Service) ListNegotiations(ctx context.Context, status, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	// Only rides that have at least one negotiation round
	wheres = append(wheres, "EXISTS (SELECT 1 FROM negotiation_rounds nr WHERE nr.ride_id = r.id)")

	if status != "" && status != "All" {
		switch status {
		case "Agreed":
			wheres = append(wheres, "r.agreed_fare IS NOT NULL")
		case "InProgress":
			wheres = append(wheres, "r.status = 'NEGOTIATING'")
		case "Failed":
			wheres = append(wheres, "r.status = 'CANCELLED' AND EXISTS (SELECT 1 FROM negotiation_rounds WHERE ride_id = r.id)")
		}
	}
	if search != "" {
		wheres = append(wheres, fmt.Sprintf("(cu.phone_number ILIKE $%d OR du.phone_number ILIKE $%d)", n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	base := `FROM rides r
		JOIN users cu ON cu.id = r.customer_id
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id`
	where := buildWhere(wheres)

	var total int
	_ = s.db.QueryRow(ctx, "SELECT COUNT(*) "+base+where, args...).Scan(&total)

	args = append(args, limit, offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.status, r.transport_type,
		       r.pickup_address, r.destination_address,
		       cu.phone_number, cu.full_name,
		       du.phone_number, du.full_name, dp.transport_type, dp.vehicle_plate,
		       r.customer_initial_fare, r.agreed_fare, r.created_at,
		       (SELECT COUNT(*) FROM negotiation_rounds WHERE ride_id = r.id) AS round_count
		%s %s ORDER BY r.created_at DESC LIMIT $%d OFFSET $%d
	`, base, where, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, rStatus, rType, pickupAddr, destAddr, custPhone string
		var custName, driverPhone, driverName, driverType, plate *string
		var initialFare, agreedFare *float64
		var createdAt time.Time
		var roundCount int
		if err := rows.Scan(&id, &rStatus, &rType, &pickupAddr, &destAddr,
			&custPhone, &custName,
			&driverPhone, &driverName, &driverType, &plate,
			&initialFare, &agreedFare, &createdAt, &roundCount); err != nil {
			return nil, 0, err
		}

		negStatus := "InProgress"
		if agreedFare != nil {
			negStatus = "Agreed"
		} else if rStatus == "CANCELLED" {
			negStatus = "Failed"
		}

		var uplift float64
		if agreedFare != nil && initialFare != nil {
			uplift = *agreedFare - *initialFare
		}

		result = append(result, map[string]interface{}{
			"id": id, "ride_id": id, "status": negStatus,
			"transport_type":      rType,
			"pickup_address":      pickupAddr,
			"destination_address": destAddr,
			"customer":            map[string]interface{}{"phone": custPhone, "name": custName},
			"driver":              map[string]interface{}{"phone": driverPhone, "name": driverName, "vehicle_type": driverType, "plate": plate},
			"initial_fare":        initialFare, "agreed_fare": agreedFare,
			"uplift": uplift, "rounds": roundCount, "created_at": createdAt,
		})
	}
	return result, total, nil
}

func (s *Service) NegotiationsStats(ctx context.Context) (map[string]interface{}, error) {
	var totalToday, agreedToday, failedToday int
	var avgRounds float64
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides WHERE created_at >= CURRENT_DATE
		AND EXISTS (SELECT 1 FROM negotiation_rounds WHERE ride_id = rides.id)
	`).Scan(&totalToday)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides WHERE created_at >= CURRENT_DATE
		AND agreed_fare IS NOT NULL
		AND EXISTS (SELECT 1 FROM negotiation_rounds WHERE ride_id = rides.id)
	`).Scan(&agreedToday)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides WHERE created_at >= CURRENT_DATE
		AND status = 'CANCELLED'
		AND EXISTS (SELECT 1 FROM negotiation_rounds WHERE ride_id = rides.id)
	`).Scan(&failedToday)
	_ = s.db.QueryRow(ctx, `
		SELECT COALESCE(AVG(cnt), 0) FROM (
			SELECT COUNT(*) AS cnt FROM negotiation_rounds
			WHERE created_at >= CURRENT_DATE GROUP BY ride_id
		) sub
	`).Scan(&avgRounds)
	return map[string]interface{}{
		"total_today":  totalToday,
		"agreed_today": agreedToday,
		"failed_today": failedToday,
		"avg_rounds":   avgRounds,
	}, nil
}

func (s *Service) GetNegotiation(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return s.GetRide(ctx, rideID)
}
