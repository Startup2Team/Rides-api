package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Admin ride oversight: history, detail, live rides and intervention.

func (s *Service) ListRides(ctx context.Context, status, transportType, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	if status != "" {
		wheres = append(wheres, fmt.Sprintf("r.status = $%d", n))
		args = append(args, status)
		n++
	}
	if transportType != "" {
		wheres = append(wheres, fmt.Sprintf("r.transport_type = $%d", n))
		args = append(args, transportType)
		n++
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
		       r.customer_id, cu.phone_number, cu.full_name,
		       r.driver_id, du.phone_number, du.full_name,
		       r.pickup_address, r.destination_address,
		       r.agreed_fare, r.customer_initial_fare,
		       r.estimated_distance_km, r.created_at, r.completed_at
		%s %s ORDER BY r.created_at DESC LIMIT $%d OFFSET $%d
	`, base, where, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, status2, tType, custID, custPhone, pickupAddr, destAddr string
		var custName, driverID, driverPhone, driverName *string
		var agreedFare, initialFare, distKm *float64
		var createdAt time.Time
		var completedAt *time.Time
		if err := rows.Scan(&id, &status2, &tType,
			&custID, &custPhone, &custName,
			&driverID, &driverPhone, &driverName,
			&pickupAddr, &destAddr,
			&agreedFare, &initialFare, &distKm,
			&createdAt, &completedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "status": status2, "transport_type": tType,
			"customer":       map[string]interface{}{"id": custID, "phone": custPhone, "name": custName},
			"driver":         map[string]interface{}{"id": driverID, "phone": driverPhone, "name": driverName},
			"pickup_address": pickupAddr, "destination_address": destAddr,
			"agreed_fare": agreedFare, "initial_fare": initialFare,
			"distance_km": distKm, "created_at": createdAt, "completed_at": completedAt,
		})
	}
	return result, total, nil
}

func (s *Service) GetRide(ctx context.Context, rideID string) (map[string]interface{}, error) {
	var id, status, tType, custID, custPhone, pickupAddr, destAddr string
	var custName, driverID, driverPhone, driverName, plate *string
	var agreedFare, initialFare, distKm *float64
	var createdAt time.Time
	var completedAt *time.Time

	err := s.db.QueryRow(ctx, `
		SELECT r.id, r.status, r.transport_type,
		       r.customer_id, cu.phone_number, cu.full_name,
		       r.driver_id, du.phone_number, du.full_name, dp.vehicle_plate,
		       r.pickup_address, r.destination_address,
		       r.agreed_fare, r.customer_initial_fare,
		       r.estimated_distance_km, r.created_at, r.completed_at
		FROM rides r
		JOIN users cu ON cu.id = r.customer_id
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id
		WHERE r.id = $1
	`, rideID).Scan(&id, &status, &tType,
		&custID, &custPhone, &custName,
		&driverID, &driverPhone, &driverName, &plate,
		&pickupAddr, &destAddr,
		&agreedFare, &initialFare, &distKm,
		&createdAt, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	negRows, _ := s.db.Query(ctx, `
		SELECT round_number, proposed_by, proposed_amount, response, created_at
		FROM negotiation_rounds WHERE ride_id = $1 ORDER BY round_number ASC
	`, rideID)
	var negotiations []map[string]interface{}
	if negRows != nil {
		defer negRows.Close()
		for negRows.Next() {
			var rn int
			var proposedBy string
			var response *string
			var amount float64
			var rAt time.Time
			if err := negRows.Scan(&rn, &proposedBy, &amount, &response, &rAt); err == nil {
				negotiations = append(negotiations, map[string]interface{}{
					"round": rn, "proposed_by": proposedBy,
					"amount": amount, "response": response, "at": rAt,
				})
			}
		}
	}

	evtRows, _ := s.db.Query(ctx, `
		SELECT event_type, actor_role, occurred_at FROM ride_events
		WHERE ride_id = $1 ORDER BY occurred_at ASC
	`, rideID)
	var events []map[string]interface{}
	if evtRows != nil {
		defer evtRows.Close()
		for evtRows.Next() {
			var eType, aRole string
			var eAt time.Time
			if err := evtRows.Scan(&eType, &aRole, &eAt); err == nil {
				events = append(events, map[string]interface{}{
					"type": eType, "actor_role": aRole, "at": eAt,
				})
			}
		}
	}

	return map[string]interface{}{
		"id": id, "status": status, "transport_type": tType,
		"customer":       map[string]interface{}{"id": custID, "phone": custPhone, "name": custName},
		"driver":         map[string]interface{}{"id": driverID, "phone": driverPhone, "name": driverName, "plate": plate},
		"pickup_address": pickupAddr, "destination_address": destAddr,
		"agreed_fare": agreedFare, "initial_fare": initialFare, "distance_km": distKm,
		"created_at": createdAt, "completed_at": completedAt,
		"negotiation_rounds": negotiations, "events": events,
	}, nil
}

func (s *Service) LiveRidesStats(ctx context.Context) (map[string]interface{}, error) {
	type row struct {
		label string
		val   *int
	}
	var total, searching, negotiating, driverEnRoute, onTrip int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE status IN ('SEARCHING','DRIVER_FOUND','DRIVER_EN_ROUTE','DRIVER_ARRIVED','NEGOTIATING','ON_TRIP')`).Scan(&total)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE status = 'SEARCHING'`).Scan(&searching)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE status = 'NEGOTIATING'`).Scan(&negotiating)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE status IN ('DRIVER_EN_ROUTE','DRIVER_ARRIVED')`).Scan(&driverEnRoute)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM rides WHERE status = 'ON_TRIP'`).Scan(&onTrip)
	return map[string]interface{}{
		"total": total, "searching": searching,
		"negotiating": negotiating, "driver_en_route": driverEnRoute, "on_trip": onTrip,
	}, nil
}

var liveStatuses = []string{"SEARCHING", "DRIVER_FOUND", "DRIVER_EN_ROUTE", "DRIVER_ARRIVED", "NEGOTIATING", "ON_TRIP"}

func (s *Service) ListLiveRides(ctx context.Context, status, district, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	if status != "" && status != "all" {
		wheres = append(wheres, fmt.Sprintf("r.status = $%d", n))
		args = append(args, status)
		n++
	} else {
		placeholders := make([]string, len(liveStatuses))
		for i, s := range liveStatuses {
			placeholders[i] = fmt.Sprintf("$%d", n)
			args = append(args, s)
			n++
		}
		wheres = append(wheres, fmt.Sprintf("r.status IN (%s)", strings.Join(placeholders, ",")))
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
		       r.customer_id, cu.phone_number, cu.full_name,
		       r.driver_id, du.phone_number, du.full_name, dp.vehicle_plate,
		       r.pickup_address, r.destination_address,
		       r.agreed_fare, r.customer_initial_fare,
		       r.estimated_distance_km, r.created_at
		%s %s ORDER BY r.created_at DESC LIMIT $%d OFFSET $%d
	`, base, where, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, status2, tType, custID, custPhone, pickupAddr, destAddr string
		var custName, driverID, driverPhone, driverName, plate *string
		var agreedFare, initialFare, distKm *float64
		var createdAt time.Time
		if err := rows.Scan(&id, &status2, &tType,
			&custID, &custPhone, &custName,
			&driverID, &driverPhone, &driverName, &plate,
			&pickupAddr, &destAddr,
			&agreedFare, &initialFare, &distKm,
			&createdAt); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "status": status2, "transport_type": tType,
			"customer":       map[string]interface{}{"id": custID, "phone": custPhone, "name": custName},
			"driver":         map[string]interface{}{"id": driverID, "phone": driverPhone, "name": driverName, "plate": plate},
			"pickup_address": pickupAddr, "destination_address": destAddr,
			"agreed_fare": agreedFare, "initial_fare": initialFare,
			"distance_km": distKm, "created_at": createdAt,
		})
	}
	return result, total, nil
}

func (s *Service) GetLiveRide(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return s.GetRide(ctx, rideID)
}

func (s *Service) InterveneRide(ctx context.Context, rideID, action, reason string) error {
	switch action {
	case "cancel":
		_, err := s.db.Exec(ctx,
			`UPDATE rides SET status='CANCELLED', updated_at=NOW() WHERE id=$1`, rideID)
		return err
	case "force-complete":
		_, err := s.db.Exec(ctx,
			`UPDATE rides SET status='COMPLETED', completed_at=NOW(), updated_at=NOW() WHERE id=$1`, rideID)
		return err
	default:
		return apperrors.New(http.StatusBadRequest, "INVALID_ACTION", "action must be cancel or force-complete")
	}
}
