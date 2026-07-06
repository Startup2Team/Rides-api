package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// DBTX is the minimal database interface the Service requires.
// *pgxpool.Pool satisfies this interface automatically.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BonusService grants the registration bonus when a driver is approved.
type BonusService interface {
	GrantRegistrationBonus(ctx context.Context, driverID, vehicleTypeID string) (any, error)
}

// PackagesService grants the free-trial credit when a driver is first approved.
type PackagesService interface {
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
}

// Service handles admin business logic.
type Service struct {
	db       DBTX
	log      zerolog.Logger
	packages PackagesService
	rdb      *goredis.Client
	bonus    BonusService
}

func NewService(db DBTX, log zerolog.Logger) *Service {
	return &Service{db: db, log: log}
}

func (s *Service) SetPackagesService(svc PackagesService) { s.packages = svc }
func (s *Service) SetBonusService(svc BonusService)       { s.bonus = svc }

// SetRedis wires the Redis client used by account-assist operations
// (clearing OTP lockouts, GPS anomaly counters).
func (s *Service) SetRedis(rdb *goredis.Client) {
	s.rdb = rdb
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
		SET approval_status = 'APPROVED',
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

	// Grant the 30-ride registration bonus (separate from the free-trial package credit).
	if s.bonus != nil {
		// Look up the vehicle_type_id for the transport_type code.
		var vehicleTypeID string
		_ = s.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, transportType).Scan(&vehicleTypeID)
		if vehicleTypeID != "" {
			if _, err := s.bonus.GrantRegistrationBonus(ctx, driverUserID, vehicleTypeID); err != nil {
				s.log.Warn().Err(err).Str("driver_user_id", driverUserID).Msg("admin: registration bonus grant failed")
			}
		}
	}
	return nil
}

func (s *Service) RejectDriver(ctx context.Context, profileID, adminUserID, reason string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'REJECTED',
		    approved_by = $1,
		    rejection_reason = $2,
		    updated_at = NOW()
		WHERE id = $3 AND approval_status = 'PENDING_REVIEW'
	`, adminUserID, reason, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.Newf(http.StatusConflict, "INVALID_STATE",
			"driver is not pending review or does not exist")
	}
	return nil
}

// RequestDriverMoreInfo puts a pending driver into NEEDS_MORE_INFO with a note
// explaining what's missing, so they re-submit. Treated as not-approved by the
// driver side, so they stay blocked from going online until re-reviewed.
func (s *Service) RequestDriverMoreInfo(ctx context.Context, profileID, adminUserID, reason string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'NEEDS_MORE_INFO',
		    approved_by = $1,
		    rejection_reason = $2,
		    updated_at = NOW()
		WHERE id = $3 AND approval_status = 'PENDING_REVIEW'
	`, adminUserID, reason, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.Newf(http.StatusConflict, "INVALID_STATE",
			"driver is not pending review or does not exist")
	}
	return nil
}

func (s *Service) SuspendDriver(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)

	var transportType string
	err := s.db.QueryRow(ctx, `SELECT transport_type FROM driver_profiles WHERE id = $1`, profileID).Scan(&transportType)
	if err != nil {
		return err
	}

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

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Force offline in Redis
	if s.rdb != nil {
		s.rdb.Set(ctx, rkeys.K.DriverState(profileID), "OFFLINE", 0)
		s.rdb.ZRem(ctx, rkeys.K.DriverGeoIndex(transportType), profileID)
	}

	return nil
}

func (s *Service) ReinstateDriver(ctx context.Context, profileID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'APPROVED', suspension_reason = NULL, updated_at = NOW()
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
		       dp.is_online, dp.city, dp.created_at,
		       EXISTS(
		           SELECT 1 FROM rides r
		           WHERE r.driver_id = dp.id
		           AND r.status IN ('CONFIRMED','DRIVER_EN_ROUTE','DRIVER_ARRIVED','IN_PROGRESS')
		       ) AS on_trip
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
		var isOnline, onTrip bool
		var createdAt time.Time
		if err := rows.Scan(&id, &userID, &phone, &fullName, &transportType, &plate,
			&approvalStatus, &priorityTier, &totalRides, &acceptanceRate, &isOnline, &city, &createdAt, &onTrip); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "user_id": userID, "phone": phone, "full_name": fullName,
			"transport_type": transportType, "vehicle_plate": plate,
			"approval_status": approvalStatus, "priority_tier": priorityTier,
			"total_rides": totalRides, "acceptance_rate": acceptanceRate,
			"is_online": isOnline, "on_trip": onTrip, "city": city, "created_at": createdAt,
		})
	}
	return result, total, nil
}

// DriverOverview returns aggregate driver status counts.
func (s *Service) DriverOverview(ctx context.Context, vehicleType string) (map[string]interface{}, error) {
	var total, online, onTrip, pending, suspended int

	// Parameterized optional filter: NULL means "all vehicle types". This keeps
	// the admin-supplied vehicleType out of the SQL string entirely.
	var vtFilter *string
	if vehicleType != "" {
		vtFilter = &vehicleType
	}

	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&total)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE is_online=TRUE AND approval_status IN ('APPROVED','ACTIVE') AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&online)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='PENDING_REVIEW' AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&pending)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='SUSPENDED' AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&suspended)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT dp.id) FROM driver_profiles dp
		JOIN rides r ON r.driver_id = dp.id
		WHERE r.status = 'IN_PROGRESS' AND ($1::text IS NULL OR dp.transport_type = $1)`, vtFilter).Scan(&onTrip)

	return map[string]interface{}{
		"total": total, "online": online,
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

// ── Account assist ───────────────────────────────────────────────────────

// ClearOTPLockout removes the Redis rate-limit key blocking further OTP sends
// for a user's phone number. Used by support staff when a user is stuck after
// repeated OTP requests.
func (s *Service) ClearOTPLockout(ctx context.Context, userID string) error {
	var phone string
	err := s.db.QueryRow(ctx, `SELECT phone_number FROM users WHERE id = $1`, userID).Scan(&phone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Del(ctx, rkeys.K.OTPRateLimit(phone)).Err()
}

// ClearGPSFlags deletes recorded GPS anomalies for a driver and resets the
// Redis anomaly counter, lifting any GPS-related auto-suspension risk.
func (s *Service) ClearGPSFlags(ctx context.Context, profileID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM gps_anomalies WHERE driver_id = $1`, profileID)
	if err != nil {
		return err
	}
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Del(ctx, rkeys.K.GPSAnomalyCount(profileID)).Err()
}

// ClearDeviceCollisionFlag removes a user from a shared-device grouping by
// deleting their device session rows for that specific device. The user's
// account itself is untouched; they can re-register a device on next login.
func (s *Service) ClearDeviceCollisionFlag(ctx context.Context, userID, deviceID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM device_sessions WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	return err
}

// GetAccountTimeline returns a read-only chronological view of a user's
// account activity: rides, device sessions, and suspension history.
func (s *Service) GetAccountTimeline(ctx context.Context, userID string, limit int) (map[string]interface{}, error) {
	rideRows, err := s.db.Query(ctx, `
		SELECT id, status, transport_type, agreed_fare, created_at, completed_at, cancel_reason
		FROM rides
		WHERE customer_id = $1 OR driver_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rideRows.Close()

	var rides []map[string]interface{}
	for rideRows.Next() {
		var id, status, transportType string
		var agreedFare *float64
		var createdAt time.Time
		var completedAt *time.Time
		var cancelReason *string
		if err := rideRows.Scan(&id, &status, &transportType, &agreedFare, &createdAt, &completedAt, &cancelReason); err != nil {
			return nil, err
		}
		rides = append(rides, map[string]interface{}{
			"id": id, "status": status, "transport_type": transportType,
			"agreed_fare": agreedFare, "created_at": createdAt,
			"completed_at": completedAt, "cancel_reason": cancelReason,
		})
	}

	sessionRows, err := s.db.Query(ctx, `
		SELECT device_id, platform, created_at
		FROM device_sessions
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer sessionRows.Close()

	var sessions []map[string]interface{}
	for sessionRows.Next() {
		var deviceID, platform string
		var createdAt time.Time
		if err := sessionRows.Scan(&deviceID, &platform, &createdAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, map[string]interface{}{
			"device_id": deviceID, "platform": platform, "created_at": createdAt,
		})
	}

	var isSuspended bool
	var suspensionUntil *time.Time
	err = s.db.QueryRow(ctx,
		`SELECT is_suspended, suspension_until FROM users WHERE id = $1`, userID,
	).Scan(&isSuspended, &suspensionUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	return map[string]interface{}{
		"rides":            rides,
		"device_sessions":  sessions,
		"is_suspended":     isSuspended,
		"suspension_until": suspensionUntil,
	}, nil
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

// ── Driver detail / create / update / delete ──────────────────────────────

func (s *Service) GetDriver(ctx context.Context, profileID string) (map[string]interface{}, error) {
	var id, userID, phone, tType, plate, license, city, momoCode, approvalStatus string
	var fullName, province, district, sector, cell, village, momoProvider, merchantPayCode, suspensionReason, rejectionReason *string
	var profileImageURL *string
	var passengerSeats, loadCapacityKg *int
	var dob *time.Time
	var licenseExpiryDate, insuranceExpiryDate, authorizationExpiryDate *time.Time
	var acceptanceRate float64
	var totalRides int
	var isOnline bool
	var createdAt time.Time

	err := s.db.QueryRow(ctx, `
		SELECT dp.id, dp.user_id, u.phone_number, u.full_name, u.profile_image_url,
		       dp.transport_type, dp.vehicle_plate, dp.license_number,
		       dp.date_of_birth, dp.city,
		       dp.province, dp.district, dp.sector, dp.cell, dp.village,
		       dp.passenger_seats, dp.load_capacity_kg,
		       dp.momo_provider, dp.momo_pay_code, dp.merchant_pay_code,
		       dp.approval_status, dp.suspension_reason, dp.rejection_reason,
		       dp.acceptance_rate, dp.total_rides, dp.is_online,
		       dp.license_expiry_date, dp.insurance_expiry_date, dp.authorization_expiry_date,
		       dp.created_at
		FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		WHERE dp.id = $1
	`, profileID).Scan(
		&id, &userID, &phone, &fullName, &profileImageURL,
		&tType, &plate, &license,
		&dob, &city,
		&province, &district, &sector, &cell, &village,
		&passengerSeats, &loadCapacityKg,
		&momoProvider, &momoCode, &merchantPayCode,
		&approvalStatus, &suspensionReason, &rejectionReason,
		&acceptanceRate, &totalRides, &isOnline,
		&licenseExpiryDate, &insuranceExpiryDate, &authorizationExpiryDate,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	// Uploaded KYC documents (licence, national ID, insurance, authorization,
	// selfie) so the admin can review the actual photos before approving.
	docs, _ := s.listDriverDocuments(ctx, id)

	return map[string]interface{}{
		"id": id, "user_id": userID, "phone": phone, "full_name": fullName,
		"transport_type": tType, "vehicle_plate": plate, "license_number": license,
		"date_of_birth": dob, "city": city,
		"address": map[string]interface{}{
			"province": province, "district": district, "sector": sector,
			"cell": cell, "village": village,
		},
		"passenger_seats": passengerSeats, "load_capacity_kg": loadCapacityKg,
		"momo_provider": momoProvider, "momo_pay_code": momoCode,
		"merchant_pay_code": merchantPayCode, "profile_image_url": profileImageURL,
		"approval_status": approvalStatus, "suspension_reason": suspensionReason,
		"rejection_reason": rejectionReason,
		"acceptance_rate":  acceptanceRate, "total_rides": totalRides, "is_online": isOnline,
		"license_expiry_date":       licenseExpiryDate,
		"insurance_expiry_date":     insuranceExpiryDate,
		"authorization_expiry_date": authorizationExpiryDate,
		"created_at":                createdAt,
		"documents":                 docs,
	}, nil
}

func (s *Service) listDriverDocuments(ctx context.Context, profileID string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT document_type, file_url, uploaded_at
		FROM driver_documents WHERE driver_id = $1
		ORDER BY uploaded_at DESC
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var docType, fileURL string
		var uploadedAt time.Time
		if err := rows.Scan(&docType, &fileURL, &uploadedAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"document_type": docType,
			"file_url":      fileURL,
			"uploaded_at":   uploadedAt,
		})
	}
	return result, nil
}

func (s *Service) UpsertDriverDocument(ctx context.Context, profileID, documentType, fileURL string) error {
	if !allowedDriverDocumentTypes[documentType] {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "unsupported document_type")
	}
	if fileURL == "" {
		return apperrors.ErrBadRequest
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE id = $1)`, profileID).Scan(&exists); err != nil || !exists {
		return apperrors.ErrNotFound
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, document_type, file_url)
		VALUES ($1, $2, $3)
		ON CONFLICT (driver_id, document_type)
		DO UPDATE SET file_url = EXCLUDED.file_url, uploaded_at = NOW()
	`, profileID, documentType, fileURL)
	if err != nil {
		return err
	}
	if documentType == "PROFILE_SELFIE" {
		_, _ = s.db.Exec(ctx, `
			UPDATE users SET profile_image_url = $1, updated_at = NOW()
			WHERE id = (SELECT user_id FROM driver_profiles WHERE id = $2)
		`, fileURL, profileID)
	}
	return nil
}

func (s *Service) UpdateDriver(ctx context.Context, profileID string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	var setClauses []string
	var args []interface{}
	n := 1
	for k, v := range fields {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, n))
		args = append(args, v)
		n++
	}
	args = append(args, profileID)
	query := fmt.Sprintf("UPDATE driver_profiles SET %s, updated_at=NOW() WHERE id = $%d",
		strings.Join(setClauses, ", "), n)
	_, err := s.db.Exec(ctx, query, args...)
	return err
}

func (s *Service) DeleteDriver(ctx context.Context, profileID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM driver_profiles WHERE id = $1`, profileID)
	return err
}

// DriverDocumentInput represents a single document to attach during driver registration.
type DriverDocumentInput struct {
	DocumentType string
	FileURL      string
}

// AdminCreateDriverInput holds the payload for admin-registered drivers.
type AdminCreateDriverInput struct {
	AdminUserID     string
	FullName        string
	Phone           string
	TransportType   string
	VehiclePlate    string
	LicenseNumber   string
	DateOfBirth     string
	Province        string
	District        string
	Sector          string
	Cell            string
	Village         string
	City            string
	MomoProvider    string
	MomoPayCode     string
	MerchantPayCode string
	ProfileImageURL string
	PassengerSeats  *int
	LoadCapacityKg  *int
	Documents       []DriverDocumentInput
}

// Allowed driver document types (aligned with mobile onboarding + admin registration).
var allowedDriverDocumentTypes = map[string]bool{
	"LICENCE_FRONT":              true,
	"LICENCE_BACK":               true,
	"VEHICLE_INSURANCE":          true,
	"VEHICLE_INSURANCE_BACK":     true,
	"VEHICLE_AUTHORIZATION":      true,
	"VEHICLE_AUTHORIZATION_BACK": true,
	"PROFILE_SELFIE":             true,
}

// CreateDriverFromAdmin registers a new driver (user + profile) from the admin panel.
// If a user with the phone already exists, reuse their account.
func (s *Service) CreateDriverFromAdmin(ctx context.Context, in AdminCreateDriverInput) (map[string]interface{}, error) {
	// 1. Find or create the user record
	var userID string
	err := s.db.QueryRow(ctx,
		`SELECT id FROM users WHERE phone_number = $1`, in.Phone).Scan(&userID)
	if err != nil {
		// User not found — create one
		err = s.db.QueryRow(ctx, `
			INSERT INTO users (phone_number, full_name, role_state)
			VALUES ($1, $2, 'DRIVER_ACTIVE')
			RETURNING id`,
			in.Phone, in.FullName,
		).Scan(&userID)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	} else {
		// User exists — promote to DRIVER_ACTIVE
		_, _ = s.db.Exec(ctx,
			`UPDATE users SET role_state = 'DRIVER_ACTIVE', updated_at = NOW() WHERE id = $1`, userID)
	}

	dob := in.DateOfBirth
	if dob == "" {
		dob = "1990-01-01"
	}
	city := in.City
	if city == "" {
		city = "Kigali"
	}

	var existingProfileID string
	if err := s.db.QueryRow(ctx,
		`SELECT id FROM driver_profiles WHERE user_id = $1`, userID,
	).Scan(&existingProfileID); err == nil {
		return nil, apperrors.Newf(http.StatusConflict, "DRIVER_ALREADY_EXISTS",
			"This phone number already has a driver registration")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing driver profile: %w", err)
	}

	if in.ProfileImageURL != "" {
		_, _ = s.db.Exec(ctx,
			`UPDATE users SET profile_image_url = $1, updated_at = NOW() WHERE id = $2`,
			in.ProfileImageURL, userID)
	}

	momoCode := in.MomoPayCode
	if momoCode == "" {
		momoCode = "—"
	}
	merchantCode := in.MerchantPayCode
	momoProvider := in.MomoProvider
	if momoProvider == "" {
		momoProvider = "mtn"
	}

	// 2. Create the driver profile — admin registration is pre-approved
	var profileID string
	err = s.db.QueryRow(ctx, `
		INSERT INTO driver_profiles (
			user_id, transport_type, vehicle_plate, license_number,
			date_of_birth, city, momo_provider, momo_pay_code, merchant_pay_code,
			approval_status, approved_by, approved_at,
			province, district, sector, cell, village,
			passenger_seats, load_capacity_kg
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,'APPROVED',$10,NOW(),$11,$12,$13,$14,$15,$16,$17
		) RETURNING id`,
		userID, in.TransportType, in.VehiclePlate, in.LicenseNumber,
		dob, city, momoProvider, momoCode, merchantCode,
		in.AdminUserID,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg,
	).Scan(&profileID)
	if err != nil {
		return nil, mapAdminCreateDriverError(err, in)
	}

	// Attach documents — mirrors mobile step 2 (license, insurance, authorization)
	for _, doc := range in.Documents {
		if err := s.UpsertDriverDocument(ctx, profileID, doc.DocumentType, doc.FileURL); err != nil {
			s.log.Warn().Err(err).Str("document_type", doc.DocumentType).Msg("admin: failed to attach document during driver registration")
		}
	}

	return map[string]interface{}{
		"id":              profileID,
		"user_id":         userID,
		"transport_type":  in.TransportType,
		"vehicle_plate":   in.VehiclePlate,
		"approval_status": "APPROVED",
		"documents_saved": len(in.Documents),
		"message":         "Driver registered and approved.",
	}, nil
}

func mapAdminCreateDriverError(err error, in AdminCreateDriverInput) error {
	if err == nil {
		return apperrors.ErrInternal
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "driver_profiles_user_id_key"):
		return apperrors.Newf(http.StatusConflict, "DRIVER_ALREADY_EXISTS",
			"This phone number already has a driver registration")
	case strings.Contains(msg, "driver_profiles_vehicle_plate_key"):
		return apperrors.Newf(http.StatusConflict, "PLATE_ALREADY_EXISTS",
			"Vehicle plate %s is already registered to another driver", in.VehiclePlate)
	case strings.Contains(msg, "driver_profiles_license_number_key"):
		return apperrors.Newf(http.StatusConflict, "LICENSE_ALREADY_EXISTS",
			"Licence number %s is already registered to another driver", in.LicenseNumber)
	case strings.Contains(msg, "23505"):
		return apperrors.Newf(http.StatusConflict, "CONFLICT",
			"A driver with this phone, plate, or licence number already exists")
	default:
		return apperrors.Newf(http.StatusInternalServerError, "INTERNAL",
			"Could not create driver profile")
	}
}

// ForceDriverOffline sets is_online=false for a driver.
func (s *Service) ForceDriverOffline(ctx context.Context, profileID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE driver_profiles SET is_online = FALSE, updated_at = NOW() WHERE id = $1`, profileID)
	return err
}

// ── Customer update / ban ─────────────────────────────────────────────────

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
	return err
}

// ── Live rides ────────────────────────────────────────────────────────────

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

// ── Negotiation detail ────────────────────────────────────────────────────

func (s *Service) GetNegotiation(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return s.GetRide(ctx, rideID)
}

// ── Revenue (unified) ─────────────────────────────────────────────────────

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
