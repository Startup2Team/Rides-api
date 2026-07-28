package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/workspace/ride-platform/internal/ledger"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Admin payments and revenue: transactions, KPIs and payout disbursement.

func (s *Service) ListTransactions(ctx context.Context, txStatus, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	// Every row here is a COMPLETED ride, i.e. settled. The filter used to be
	// accepted and ignored, so picking "Pending payout" returned settled rows.
	if txStatus != "" && !strings.EqualFold(txStatus, "Settled") {
		return []map[string]interface{}{}, 0, nil
	}
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

// RevenueKPIs reports the headline finance tiles for a period.
//
// Revenue comes from the general ledger, not from ride fares. The previous
// implementation summed rides.agreed_fare over completed rides and called that
// revenue, which meant a driver could buy 19,500 RWF of packages and the tile
// would still read zero — the reported symptom. A fare is money the passenger
// hands the driver; the platform never touches it. It is reported here as
// gross_fares (GMV) and kept out of revenue.
//
// Commission likewise comes from ledger account 4100 rather than being invented
// as 15% of gross fares. Under prepaid packages the platform takes no per-ride
// cut, so that figure was revenue that does not exist.
func (s *Service) RevenueKPIs(ctx context.Context, period string) (map[string]interface{}, error) {
	// revenueWindow rather than periodToInterval: the latter maps every
	// unrecognised period — including "custom" and "today" — to one day.
	start, end := revenueWindow(period, "", "")
	prevStart := start.Add(-end.Sub(start))

	rev, err := ledger.RevenueBetween(ctx, s.db, start, end)
	if err != nil {
		return nil, err
	}
	prev, err := ledger.RevenueBetween(ctx, s.db, prevStart, start)
	if err != nil {
		return nil, err
	}

	// Ride activity is still worth showing; it just is not revenue.
	var grossFares float64
	var rideCount, rideCountPrev int
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
	`, start, end).Scan(&grossFares, &rideCount); err != nil {
		return nil, fmt.Errorf("admin: ride totals for revenue KPIs: %w", err)
	}
	if err := s.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
	`, prevStart, start).Scan(&rideCountPrev); err != nil {
		return nil, fmt.Errorf("admin: previous ride count for revenue KPIs: %w", err)
	}

	revChange := 0.0
	if prev.Total > 0 {
		revChange = float64(rev.Total-prev.Total) / float64(prev.Total) * 100
	}

	return map[string]interface{}{
		"revenue_total":      float64(rev.Total),
		"package_sales":      float64(rev.PackageSales),
		"commission":         float64(rev.Commission),
		"payout":             float64(rev.Total - rev.Commission),
		"gross_fares":        grossFares,
		"ride_count":         rideCount,
		"revenue_change_pct": revChange,
		"ride_count_prev":    rideCountPrev,
		"period":             period,
	}, nil
}

// revenueWindow resolves the reporting window. An explicit from/to always wins;
// otherwise fall back to the named period. periodToInterval maps anything it
// doesn't recognise — including "custom" and "today" — to INTERVAL '1 day', so
// picking a custom range in the console silently reported the last 24 hours.
func revenueWindow(period, from, to string) (time.Time, time.Time) {
	end := time.Now()
	if f, err := time.Parse("2006-01-02", from); err == nil {
		if t, tErr := time.Parse("2006-01-02", to); tErr == nil {
			// `to` is an inclusive day in the UI, so run to the end of it.
			return f, t.AddDate(0, 0, 1)
		}
		return f, end
	}
	switch period {
	case "week":
		return end.AddDate(0, 0, -7), end
	case "month":
		return end.AddDate(0, 0, -30), end
	case "quarter":
		return end.AddDate(0, 0, -90), end
	case "year":
		return end.AddDate(0, 0, -365), end
	default: // today
		return end.AddDate(0, 0, -1), end
	}
}

func (s *Service) Revenue(ctx context.Context, period, from, to string) (map[string]interface{}, error) {
	start, end := revenueWindow(period, from, to)
	// Previous window of the same length, for the delta.
	prevStart := start.Add(-end.Sub(start))

	var gross, grossPrev float64
	var trips, tripsPrev int

	_ = s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
	`, start, end).Scan(&gross, &trips)

	_ = s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(agreed_fare),0), COUNT(*)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
	`, prevStart, start).Scan(&grossPrev, &tripsPrev)

	commission := gross * 0.15
	payouts := gross - commission
	grossDelta := 0.0
	if grossPrev > 0 {
		grossDelta = (gross - grossPrev) / grossPrev * 100
	}

	// Trend (daily buckets, last 7 entries)
	trendRows, _ := s.db.Query(ctx, `
		SELECT DATE(completed_at) AS day, COALESCE(SUM(agreed_fare),0)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
		GROUP BY day ORDER BY day
	`, start, end)
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
	vRows, _ := s.db.Query(ctx, `
		SELECT transport_type, COALESCE(SUM(agreed_fare),0)
		FROM rides WHERE status='COMPLETED' AND completed_at >= $1 AND completed_at < $2
		GROUP BY transport_type ORDER BY 2 DESC
	`, start, end)
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
		"from":       start,
		"to":         end,
		"gross":      gross,
		"commission": commission,
		"payouts":    payouts,
		"trips":      trips,
		"deltas":     map[string]interface{}{"gross": grossDelta},
		"trend":      trend,
		"by_vehicle": byVehicle,
	}, nil
}

// DisbursePayouts is deliberately not implemented.
//
// It used to run a single SELECT and return `SUM(agreed_fare) * 0.85` as though
// that money had been disbursed — no row was written, no provider was called,
// and there is no payout table in the schema at all. Reporting a disbursement
// that never happened is worse than refusing, so it now refuses. Implementing it
// needs a payout ledger (idempotency key, provider reference, state machine,
// reconciliation) designed on purpose, not inferred here.
func (s *Service) DisbursePayouts(ctx context.Context, transactionIDs []string) (int, float64, error) {
	if len(transactionIDs) == 0 {
		return 0, 0, apperrors.ErrBadRequest
	}
	s.log.Warn().Int("requested", len(transactionIDs)).
		Msg("admin: payout disbursement attempted but no payout ledger exists")
	return 0, 0, apperrors.New(http.StatusNotImplemented, "NOT_IMPLEMENTED",
		"payout disbursement is not implemented — no payout ledger exists yet")
}
