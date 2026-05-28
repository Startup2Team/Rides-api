package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// PackagesService grants the free-trial credit when a driver is first approved.
type PackagesService interface {
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
}

// Service handles admin business logic.
type Service struct {
	db       *pgxpool.Pool
	log      zerolog.Logger
	packages PackagesService
}

func NewService(db *pgxpool.Pool, log zerolog.Logger) *Service {
	return &Service{db: db, log: log}
}

func (s *Service) SetPackagesService(svc PackagesService) {
	s.packages = svc
}

// ── Driver management ─────────────────────────────────────────────────────

func (s *Service) ApproveDriver(ctx context.Context, profileID, adminUserID string) error {
	var driverUserID, transportType string
	err := s.db.QueryRow(ctx,
		`SELECT user_id, transport_type FROM driver_profiles WHERE id = $1`, profileID,
	).Scan(&driverUserID, &transportType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	if driverUserID == adminUserID {
		return apperrors.ErrSelfApproval
	}

	_, err = s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'ACTIVE',
		    approved_by = $1,
		    approved_at = NOW(),
		    rejection_reason = NULL,
		    updated_at = NOW()
		WHERE id = $2 AND approval_status = 'PENDING_REVIEW'
	`, adminUserID, profileID)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		UPDATE users u
		SET role_state = 'DRIVER_ACTIVE', updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $1 AND u.id = dp.user_id
	`, profileID)
	if err != nil {
		return err
	}

	if s.packages != nil {
		if err := s.packages.GrantFreeTrialIfEligible(ctx, driverUserID, transportType); err != nil {
			s.log.Error().Err(err).
				Str("driver_user_id", driverUserID).
				Str("transport_type", transportType).
				Msg("admin: free trial grant failed after approval")
		}
	}
	return nil
}

func (s *Service) RejectDriver(ctx context.Context, profileID, adminUserID, reason string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'REJECTED',
		    approved_by = $1,
		    rejection_reason = $2,
		    updated_at = NOW()
		WHERE id = $3
	`, adminUserID, reason, profileID)
	return err
}

func (s *Service) SuspendDriver(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'SUSPENDED',
		    suspension_reason = $1,
		    is_online = FALSE,
		    updated_at = NOW()
		WHERE id = $2
	`, reason, profileID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET is_suspended = TRUE,
		    suspension_until = $1,
		    role_state = 'DRIVER_SUSPENDED',
		    updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $2 AND u.id = dp.user_id
	`, suspendedUntil, profileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Service) ReinstateDriver(ctx context.Context, profileID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'ACTIVE', suspension_reason = NULL, updated_at = NOW()
		WHERE id = $1
	`, profileID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET is_suspended = FALSE, suspension_until = NULL, role_state = 'DRIVER_ACTIVE', updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $1 AND u.id = dp.user_id
	`, profileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ListDrivers returns paginated driver profiles, filterable by status, vehicle type, and search.
func (s *Service) ListDrivers(ctx context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	if status != "" {
		wheres = append(wheres, fmt.Sprintf("dp.approval_status = $%d", n))
		args = append(args, status)
		n++
	}
	if vehicleType != "" {
		wheres = append(wheres, fmt.Sprintf("dp.transport_type = $%d", n))
		args = append(args, vehicleType)
		n++
	}
	if search != "" {
		wheres = append(wheres, fmt.Sprintf(
			"(u.phone_number ILIKE $%d OR u.full_name ILIKE $%d OR dp.vehicle_plate ILIKE $%d)", n, n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	base := `FROM driver_profiles dp JOIN users u ON u.id = dp.user_id`
	where := buildWhere(wheres)

	var total int
	_ = s.db.QueryRow(ctx, "SELECT COUNT(*) "+base+where, args...).Scan(&total)

	orderBy := "dp.created_at DESC"
	switch sort {
	case "acceptance_rate":
		orderBy = "dp.acceptance_rate DESC"
	case "total_rides":
		orderBy = "dp.total_rides DESC"
	case "name":
		orderBy = "u.full_name ASC"
	}

	args = append(args, limit, offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT dp.id, dp.user_id, u.phone_number, u.full_name,
		       dp.transport_type, dp.vehicle_plate, dp.approval_status,
		       dp.priority_tier, dp.total_rides, dp.acceptance_rate,
		       dp.is_online, dp.city, dp.created_at
		%s %s ORDER BY %s LIMIT $%d OFFSET $%d
	`, base, where, orderBy, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, userID, phone, transportType, plate, approvalStatus string
		var fullName *string
		var city *string
		var priorityTier, totalRides int
		var acceptanceRate float64
		var isOnline bool
		var createdAt time.Time
		if err := rows.Scan(&id, &userID, &phone, &fullName, &transportType, &plate,
			&approvalStatus, &priorityTier, &totalRides, &acceptanceRate, &isOnline, &city, &createdAt); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "user_id": userID, "phone": phone, "full_name": fullName,
			"transport_type": transportType, "vehicle_plate": plate,
			"approval_status": approvalStatus, "priority_tier": priorityTier,
			"total_rides": totalRides, "acceptance_rate": acceptanceRate,
			"is_online": isOnline, "city": city, "created_at": createdAt,
		})
	}
	return result, total, nil
}

// DriverOverview returns aggregate driver status counts.
func (s *Service) DriverOverview(ctx context.Context) (map[string]interface{}, error) {
	var total, active, online, onTrip, pending, suspended int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles`).Scan(&total)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='ACTIVE'`).Scan(&active)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE is_online=TRUE AND approval_status='ACTIVE'`).Scan(&online)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='PENDING_REVIEW'`).Scan(&pending)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='SUSPENDED'`).Scan(&suspended)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT dp.id) FROM driver_profiles dp
		JOIN rides r ON r.driver_id = dp.id
		WHERE r.status = 'IN_PROGRESS'
	`).Scan(&onTrip)

	return map[string]interface{}{
		"total": total, "active": active, "online": online,
		"on_trip": onTrip, "pending": pending, "suspended": suspended,
	}, nil
}

// ── Customer management ───────────────────────────────────────────────────

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
		wheres = append(wheres, fmt.Sprintf("(u.phone_number ILIKE $%d OR u.full_name ILIKE $%d)", n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	base := `FROM users u`
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
		SELECT u.id, u.phone_number, u.full_name, u.role_state,
		       u.is_suspended, u.suspension_until, u.created_at,
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
		var fullName *string
		var isSuspended bool
		var suspensionUntil *time.Time
		var createdAt time.Time
		var totalRides int
		var totalSpend float64
		if err := rows.Scan(&id, &phone, &fullName, &roleState, &isSuspended,
			&suspensionUntil, &createdAt, &totalRides, &totalSpend); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "phone": phone, "full_name": fullName,
			"role_state": roleState, "is_suspended": isSuspended,
			"suspension_until": suspensionUntil, "created_at": createdAt,
			"total_rides": totalRides, "total_spend": totalSpend,
		})
	}
	return result, total, nil
}

func (s *Service) GetCustomer(ctx context.Context, userID string) (map[string]interface{}, error) {
	var id, phone, roleState string
	var fullName *string
	var isSuspended bool
	var suspensionUntil *time.Time
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT id, phone_number, full_name, role_state, is_suspended, suspension_until, created_at
		FROM users WHERE id = $1
	`, userID).Scan(&id, &phone, &fullName, &roleState, &isSuspended, &suspensionUntil, &createdAt)
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
		SELECT r.id, r.status, r.transport_type, r.agreed_fare, r.created_at
		FROM rides r WHERE r.customer_id=$1 ORDER BY r.created_at DESC LIMIT 10
	`, userID)
	var trips []map[string]interface{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var rID, rStatus, rType string
			var fare *float64
			var rAt time.Time
			if err := rows.Scan(&rID, &rStatus, &rType, &fare, &rAt); err == nil {
				trips = append(trips, map[string]interface{}{
					"id": rID, "status": rStatus, "transport_type": rType,
					"agreed_fare": fare, "created_at": rAt,
				})
			}
		}
	}

	return map[string]interface{}{
		"id": id, "phone": phone, "full_name": fullName,
		"role_state": roleState, "is_suspended": isSuspended,
		"suspension_until": suspensionUntil, "created_at": createdAt,
		"total_rides": totalRides, "total_spend": totalSpend,
		"recent_trips": trips,
	}, nil
}

func (s *Service) SuspendUser(ctx context.Context, userID string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)
	_, err := s.db.Exec(ctx, `
		UPDATE users SET is_suspended = TRUE, suspension_until = $1, updated_at = NOW()
		WHERE id = $2
	`, suspendedUntil, userID)
	return err
}

func (s *Service) ReinstateUser(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE users SET is_suspended = FALSE, suspension_until = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)
	return err
}

// ── Rides ─────────────────────────────────────────────────────────────────

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
			"customer": map[string]interface{}{"id": custID, "phone": custPhone, "name": custName},
			"driver":   map[string]interface{}{"id": driverID, "phone": driverPhone, "name": driverName},
			"pickup_address": pickupAddr, "destination_address": destAddr,
			"agreed_fare": agreedFare, "initial_fare": initialFare,
			"distance_km": distKm, "created_at": createdAt, "completed_at": completedAt,
		})
	}
	return result, total, nil
}

func (s *Service) GetRide(ctx context.Context, rideID string) (map[string]interface{}, error) {
	var id, status, tType, custID, custPhone, pickupAddr, destAddr string
	var custName, driverID, driverPhone, driverName *string
	var agreedFare, initialFare, distKm *float64
	var createdAt time.Time
	var completedAt *time.Time

	err := s.db.QueryRow(ctx, `
		SELECT r.id, r.status, r.transport_type,
		       r.customer_id, cu.phone_number, cu.full_name,
		       r.driver_id, du.phone_number, du.full_name,
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
		&driverID, &driverPhone, &driverName,
		&pickupAddr, &destAddr,
		&agreedFare, &initialFare, &distKm,
		&createdAt, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	rows, _ := s.db.Query(ctx, `
		SELECT round_number, proposed_by, proposed_amount, response, created_at
		FROM negotiation_rounds WHERE ride_id = $1 ORDER BY round_number ASC
	`, rideID)
	var negotiations []map[string]interface{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var rn int
			var proposedBy string
			var response *string
			var amount float64
			var rAt time.Time
			if err := rows.Scan(&rn, &proposedBy, &amount, &response, &rAt); err == nil {
				negotiations = append(negotiations, map[string]interface{}{
					"round": rn, "proposed_by": proposedBy,
					"amount": amount, "response": response, "at": rAt,
				})
			}
		}
	}

	return map[string]interface{}{
		"id": id, "status": status, "transport_type": tType,
		"customer": map[string]interface{}{"id": custID, "phone": custPhone, "name": custName},
		"driver":   map[string]interface{}{"id": driverID, "phone": driverPhone, "name": driverName},
		"pickup_address": pickupAddr, "destination_address": destAddr,
		"agreed_fare": agreedFare, "initial_fare": initialFare, "distance_km": distKm,
		"created_at": createdAt, "completed_at": completedAt,
		"negotiation_rounds": negotiations,
	}, nil
}

// ── Negotiations ──────────────────────────────────────────────────────────

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
		       cu.phone_number, cu.full_name,
		       du.phone_number, du.full_name, dp.transport_type,
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
		var id, rStatus, rType, custPhone string
		var custName, driverPhone, driverName, driverType *string
		var initialFare, agreedFare *float64
		var createdAt time.Time
		var roundCount int
		if err := rows.Scan(&id, &rStatus, &rType,
			&custPhone, &custName,
			&driverPhone, &driverName, &driverType,
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
			"transport_type": rType,
			"customer": map[string]interface{}{"phone": custPhone, "name": custName},
			"driver":   map[string]interface{}{"phone": driverPhone, "name": driverName, "vehicle_type": driverType},
			"initial_fare": initialFare, "agreed_fare": agreedFare,
			"uplift": uplift, "rounds": roundCount, "created_at": createdAt,
		})
	}
	return result, total, nil
}

// ── Revenue / transactions ────────────────────────────────────────────────

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
			"status": "Settled",
			"pickup_address": pickupAddr, "destination_address": destAddr,
			"customer": map[string]interface{}{"phone": custPhone, "name": custName},
			"driver":   map[string]interface{}{"phone": driverPhone, "name": driverName, "plate": plate, "vehicle_type": tType},
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

// ── Safety flags ──────────────────────────────────────────────────────────

func (s *Service) GPSAnomalies(ctx context.Context, limit int) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ga.id, ga.driver_id, u.phone_number, ga.computed_speed, ga.detected_at
		FROM gps_anomalies ga
		JOIN driver_profiles dp ON dp.id = ga.driver_id
		JOIN users u ON u.id = dp.user_id
		ORDER BY ga.detected_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, driverID, phone string
		var speed float64
		var detectedAt time.Time
		if err := rows.Scan(&id, &driverID, &phone, &speed, &detectedAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "driver_id": driverID, "phone": phone,
			"computed_speed_kmh": speed, "detected_at": detectedAt,
		})
	}
	return result, nil
}

func (s *Service) DeviceCollisions(ctx context.Context) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ds.device_id, COUNT(DISTINCT ds.user_id) AS user_count,
		       ARRAY_AGG(DISTINCT u.phone_number) AS phones
		FROM device_sessions ds
		JOIN users u ON u.id = ds.user_id
		GROUP BY ds.device_id
		HAVING COUNT(DISTINCT ds.user_id) > 1
		ORDER BY user_count DESC LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var deviceID string
		var userCount int
		var phones []string
		if err := rows.Scan(&deviceID, &userCount, &phones); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"device_id": deviceID, "user_count": userCount, "phones": phones,
		})
	}
	return result, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

func buildWhere(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}

func periodToInterval(period string) string {
	switch period {
	case "week":
		return "INTERVAL '7 days'"
	case "month":
		return "INTERVAL '30 days'"
	case "quarter":
		return "INTERVAL '90 days'"
	case "year":
		return "INTERVAL '365 days'"
	default:
		return "INTERVAL '1 day'"
	}
}
