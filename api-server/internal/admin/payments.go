package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Admin payments and revenue: transactions, KPIs and payout disbursement.

func (s *Service) ListTransactions(ctx context.Context, txStatus, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	base := `FROM rides r
		JOIN users cu ON cu.id = r.customer_id
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id
		WHERE r.status = 'COMPLETED'`

	var total int
	_ = s.db.QueryRow(ctx, "SELECT COUNT(*) "+base).Scan(&total)

	orderBy := "r.completed_at DESC"
	if sort == "fare" {
		orderBy = "r.agreed_fare DESC"
	}

	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.transport_type, r.agreed_fare,
		       r.pickup_address, r.destination_address,
		       cu.phone_number, cu.full_name,
		       du.phone_number, du.full_name, dp.vehicle_plate,
		       r.completed_at
		%s ORDER BY %s LIMIT $1 OFFSET $2
	`, base, orderBy), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	commissionRate := 0.15
	for rows.Next() {
		var id, tType, pickupAddr, destAddr, custPhone string
		var custName, driverPhone, driverName, plate *string
		var agreedFare *float64
		var completedAt *time.Time
		if err := rows.Scan(&id, &tType, &agreedFare,
			&pickupAddr, &destAddr,
			&custPhone, &custName,
			&driverPhone, &driverName, &plate,
			&completedAt); err != nil {
			return nil, 0, err
		}
		var commission, payout float64
		if agreedFare != nil {
			commission = *agreedFare * commissionRate
			payout = *agreedFare - commission
		}
		result = append(result, map[string]interface{}{
			"id": id, "transport_type": tType,
			"fare": agreedFare, "commission": commission, "payout": payout,
			"status":         "Settled",
			"pickup_address": pickupAddr, "destination_address": destAddr,
			"customer":     map[string]interface{}{"phone": custPhone, "name": custName},
			"driver":       map[string]interface{}{"phone": driverPhone, "name": driverName, "plate": plate, "vehicle_type": tType},
			"completed_at": completedAt,
		})
	}
	return result, total, nil
}

func (s *Service) RevenueKPIs(ctx context.Context, period string) (map[string]interface{}, error) {
	interval := periodToInterval(period)

	var revenueTotal, revenuePrev float64
	var rideCount, rideCountPrev int

	_ = s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= NOW() - %s
	`, interval)).Scan(&revenueTotal, &rideCount)

	_ = s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED'
		  AND completed_at >= NOW() - 2 * %s
		  AND completed_at <  NOW() - %s
	`, interval, interval)).Scan(&revenuePrev, &rideCountPrev)

	commission := revenueTotal * 0.15
	revChange := 0.0
	if revenuePrev > 0 {
		revChange = (revenueTotal - revenuePrev) / revenuePrev * 100
	}

	return map[string]interface{}{
		"revenue_total":      revenueTotal,
		"commission":         commission,
		"payout":             revenueTotal - commission,
		"ride_count":         rideCount,
		"revenue_change_pct": revChange,
		"ride_count_prev":    rideCountPrev,
		"period":             period,
	}, nil
}

func (s *Service) Revenue(ctx context.Context, period string) (map[string]interface{}, error) {
	interval := periodToInterval(period)

	var gross, grossPrev float64
	var trips, tripsPrev int

	_ = s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= NOW() - %s
	`, interval)).Scan(&gross, &trips)

	_ = s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED'
		  AND completed_at >= NOW() - 2*%s AND completed_at < NOW() - %s
	`, interval, interval)).Scan(&grossPrev, &tripsPrev)

	commission := gross * 0.15
	payouts := gross - commission
	grossDelta := 0.0
	if grossPrev > 0 {
		grossDelta = (gross - grossPrev) / grossPrev * 100
	}

	// Trend (daily buckets, last 7 entries)
	trendRows, _ := s.db.Query(ctx, fmt.Sprintf(`
		SELECT DATE(completed_at) AS day, COALESCE(SUM(agreed_fare),0)
		FROM rides WHERE status='COMPLETED' AND completed_at >= NOW() - %s
		GROUP BY day ORDER BY day
	`, interval))
	var trend []map[string]interface{}
	if trendRows != nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var day time.Time
			var val float64
			if err := trendRows.Scan(&day, &val); err == nil {
				trend = append(trend, map[string]interface{}{"label": day.Format("Jan 2"), "value": val})
			}
		}
	}

	// By vehicle type
	vRows, _ := s.db.Query(ctx, fmt.Sprintf(`
		SELECT transport_type, COALESCE(SUM(agreed_fare),0)
		FROM rides WHERE status='COMPLETED' AND completed_at >= NOW() - %s
		GROUP BY transport_type ORDER BY 2 DESC
	`, interval))
	var byVehicle []map[string]interface{}
	if vRows != nil {
		defer vRows.Close()
		for vRows.Next() {
			var vType string
			var amount float64
			if err := vRows.Scan(&vType, &amount); err == nil {
				pct := 0.0
				if gross > 0 {
					pct = amount / gross * 100
				}
				byVehicle = append(byVehicle, map[string]interface{}{
					"vehicle": vType, "amount": amount, "pct": pct,
				})
			}
		}
	}

	return map[string]interface{}{
		"period":     period,
		"gross":      gross,
		"commission": commission,
		"payouts":    payouts,
		"trips":      trips,
		"deltas":     map[string]interface{}{"gross": grossDelta},
		"trend":      trend,
		"by_vehicle": byVehicle,
	}, nil
}

func (s *Service) DisbursePayouts(ctx context.Context, transactionIDs []string) (int, float64, error) {
	if len(transactionIDs) == 0 {
		return 0, 0, apperrors.ErrBadRequest
	}
	placeholders := make([]string, len(transactionIDs))
	args := make([]interface{}, len(transactionIDs))
	for i, id := range transactionIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	var total float64
	_ = s.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(agreed_fare),0) FROM rides WHERE id IN (%s) AND status='COMPLETED'
	`, strings.Join(placeholders, ",")), args...).Scan(&total)

	return len(transactionIDs), total * 0.85, nil
}
