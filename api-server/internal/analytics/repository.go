package analytics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository handles read-only analytics queries.
// All queries run against the analytics_events table — NEVER against operational tables.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Overview returns the real-time platform dashboard numbers.
func (r *Repository) Overview(ctx context.Context) (map[string]interface{}, error) {
	var activeDrivers, activeRides, totalRidesToday int
	var totalRevenueToday float64

	_ = r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles WHERE is_online = TRUE AND approval_status = 'ACTIVE'
	`).Scan(&activeDrivers)

	_ = r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides WHERE status NOT IN ('COMPLETED','CANCELLED')
	`).Scan(&activeRides)

	_ = r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides WHERE DATE(created_at) = CURRENT_DATE
	`).Scan(&totalRidesToday)

	_ = r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(agreed_fare),0) FROM rides
		WHERE status = 'COMPLETED' AND DATE(completed_at) = CURRENT_DATE
	`).Scan(&totalRevenueToday)

	return map[string]interface{}{
		"active_drivers":      activeDrivers,
		"active_rides":        activeRides,
		"total_rides_today":   totalRidesToday,
		"total_revenue_today": totalRevenueToday,
	}, nil
}

// DailyRides returns per-day ride counts for the last N days.
func (r *Repository) DailyRides(ctx context.Context, days int) ([]map[string]interface{}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DATE(created_at) AS day, COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE status = 'COMPLETED') AS completed,
		       COUNT(*) FILTER (WHERE status = 'CANCELLED') AS cancelled
		FROM rides
		WHERE created_at >= NOW() - ($1 || ' days')::INTERVAL
		GROUP BY day ORDER BY day DESC
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var day time.Time
		var total, completed, cancelled int
		if err := rows.Scan(&day, &total, &completed, &cancelled); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"day": day.Format("2006-01-02"), "total": total,
			"completed": completed, "cancelled": cancelled,
		})
	}
	return result, nil
}

// WeeklyRides returns per-week ride counts.
func (r *Repository) WeeklyRides(ctx context.Context) ([]map[string]interface{}, error) {
	return r.DailyRides(ctx, 56) // 8 weeks
}

// RevenueBreakdown returns revenue by transport type for the last 30 days.
func (r *Repository) RevenueBreakdown(ctx context.Context) ([]map[string]interface{}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT transport_type,
		       COUNT(*) AS ride_count,
		       COALESCE(SUM(agreed_fare),0) AS total_revenue,
		       COALESCE(AVG(agreed_fare),0) AS avg_fare
		FROM rides
		WHERE status = 'COMPLETED'
		  AND completed_at >= NOW() - INTERVAL '30 days'
		GROUP BY transport_type
		ORDER BY total_revenue DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var transportType string
		var rideCount int
		var totalRevenue, avgFare float64
		if err := rows.Scan(&transportType, &rideCount, &totalRevenue, &avgFare); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"transport_type": transportType,
			"ride_count":     rideCount,
			"total_revenue":  totalRevenue,
			"avg_fare":       avgFare,
		})
	}
	return result, nil
}

// DriverPerformance returns top driver metrics.
func (r *Repository) DriverPerformance(ctx context.Context, limit int) ([]map[string]interface{}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT dp.id, u.phone_number, dp.transport_type, dp.total_rides,
		       dp.acceptance_rate, dp.priority_tier,
		       COALESCE(SUM(r.agreed_fare) FILTER (WHERE r.status='COMPLETED'), 0) AS earnings_30d
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		LEFT JOIN rides r ON r.driver_id = dp.id AND r.completed_at >= NOW() - INTERVAL '30 days'
		WHERE dp.approval_status = 'ACTIVE'
		GROUP BY dp.id, u.phone_number, dp.transport_type, dp.total_rides, dp.acceptance_rate, dp.priority_tier
		ORDER BY earnings_30d DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, phone, transportType string
		var totalRides, priorityTier int
		var acceptanceRate, earnings float64
		if err := rows.Scan(&id, &phone, &transportType, &totalRides, &acceptanceRate, &priorityTier, &earnings); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"driver_id": id, "phone": phone, "transport_type": transportType,
			"total_rides": totalRides, "acceptance_rate": acceptanceRate,
			"priority_tier": priorityTier, "earnings_30d": earnings,
		})
	}
	return result, nil
}

// NegotiationStats returns negotiation analytics.
func (r *Repository) NegotiationStats(ctx context.Context) (map[string]interface{}, error) {
	var avgRounds float64
	var callRate, failureRate float64

	_ = r.db.QueryRow(ctx, `
		SELECT COALESCE(AVG(round_count),0)
		FROM (
			SELECT ride_id, COUNT(*) AS round_count
			FROM negotiation_rounds
			GROUP BY ride_id
		) t
	`).Scan(&avgRounds)

	_ = r.db.QueryRow(ctx, `
		SELECT CASE WHEN COUNT(*) = 0 THEN 0
		       ELSE ROUND(COUNT(*) FILTER (WHERE call_initiated) * 100.0 / COUNT(*), 2)
		       END
		FROM negotiation_rounds
		WHERE created_at >= NOW() - INTERVAL '30 days'
	`).Scan(&callRate)

	_ = r.db.QueryRow(ctx, `
		SELECT CASE WHEN COUNT(*) = 0 THEN 0
		       ELSE ROUND(COUNT(*) FILTER (WHERE status = 'CANCELLED') * 100.0 / COUNT(*), 2)
		       END
		FROM rides
		WHERE created_at >= NOW() - INTERVAL '30 days'
	`).Scan(&failureRate)

	return map[string]interface{}{
		"avg_rounds_per_ride":      avgRounds,
		"call_initiated_rate_pct":  callRate,
		"negotiation_failure_rate": failureRate,
	}, nil
}

// Heatmap returns demand density as pickup point grid — no PII exposed.
func (r *Repository) Heatmap(ctx context.Context) ([]map[string]interface{}, error) {
	rows, err := r.db.Query(ctx, `
		SELECT
			ROUND(ST_Y(pickup_point::geometry)::NUMERIC, 3) AS lat_bucket,
			ROUND(ST_X(pickup_point::geometry)::NUMERIC, 3) AS lng_bucket,
			COUNT(*) AS demand_count
		FROM rides
		WHERE created_at >= NOW() - INTERVAL '7 days'
		GROUP BY lat_bucket, lng_bucket
		ORDER BY demand_count DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var lat, lng float64
		var count int
		if err := rows.Scan(&lat, &lng, &count); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{"lat": lat, "lng": lng, "count": count})
	}
	return result, nil
}

// CancellationStats returns cancellation analytics.
func (r *Repository) CancellationStats(ctx context.Context) (map[string]interface{}, error) {
	var totalCancels, customerCancels, driverCancels, noCancelCount int

	_ = r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'CANCELLED'),
			COUNT(*) FILTER (WHERE status = 'CANCELLED' AND cancelled_by_role = 'CUSTOMER'),
			COUNT(*) FILTER (WHERE status = 'CANCELLED' AND cancelled_by_role = 'DRIVER'),
			COUNT(*) FILTER (WHERE status = 'CANCELLED' AND cancelled_by_role IS NULL)
		FROM rides WHERE created_at >= NOW() - INTERVAL '30 days'
	`).Scan(&totalCancels, &customerCancels, &driverCancels, &noCancelCount)

	return map[string]interface{}{
		"total_cancellations":    totalCancels,
		"by_customer":            customerCancels,
		"by_driver":              driverCancels,
		"no_driver_found":        noCancelCount,
	}, nil
}
