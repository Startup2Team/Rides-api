package admin

import (
	"context"
	"errors"
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

// ApproveDriver approves a pending driver application.
// Admin cannot approve their own application (self-approval prevention).
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

	// Grant one-time free trial for this vehicle type — logged but never blocks approval.
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

// RejectDriver rejects a driver application with a reason.
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

// SuspendDriver suspends an active driver for a given number of hours.
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

// SuspendUser suspends a user account.
func (s *Service) SuspendUser(ctx context.Context, userID string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)
	_, err := s.db.Exec(ctx, `
		UPDATE users SET is_suspended = TRUE, suspension_until = $1, updated_at = NOW()
		WHERE id = $2
	`, suspendedUntil, userID)
	return err
}

// ListDrivers returns paginated driver profiles, filterable by approval_status.
func (s *Service) ListDrivers(ctx context.Context, status string, limit, offset int) ([]map[string]interface{}, error) {
	q := `
		SELECT dp.id, dp.user_id, u.phone_number, dp.transport_type,
		       dp.vehicle_plate, dp.approval_status, dp.priority_tier,
		       dp.total_rides, dp.acceptance_rate, dp.created_at
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
	`
	args := []interface{}{limit, offset}
	if status != "" {
		q += ` WHERE dp.approval_status = $3`
		args = append(args, status)
	}
	q += ` ORDER BY dp.created_at DESC LIMIT $1 OFFSET $2`

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, userID, phone, transportType, plate, approvalStatus string
		var priorityTier, totalRides int
		var acceptanceRate float64
		var createdAt time.Time
		if err := rows.Scan(&id, &userID, &phone, &transportType, &plate, &approvalStatus, &priorityTier, &totalRides, &acceptanceRate, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "user_id": userID, "phone": phone,
			"transport_type": transportType, "vehicle_plate": plate,
			"approval_status": approvalStatus, "priority_tier": priorityTier,
			"total_rides": totalRides, "acceptance_rate": acceptanceRate,
			"created_at": createdAt,
		})
	}
	return result, nil
}

// ListUsers returns paginated users.
func (s *Service) ListUsers(ctx context.Context, limit, offset int) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, phone_number, full_name, role_state, is_suspended, created_at
		FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, phone, roleState string
		var fullName *string
		var isSuspended bool
		var createdAt time.Time
		if err := rows.Scan(&id, &phone, &fullName, &roleState, &isSuspended, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "phone": phone, "full_name": fullName,
			"role_state": roleState, "is_suspended": isSuspended,
			"created_at": createdAt,
		})
	}
	return result, nil
}

// GPSAnomalies returns the GPS anomaly log for admin review.
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

// DeviceCollisions returns users flagged for device_id collisions.
func (s *Service) DeviceCollisions(ctx context.Context) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ds.device_id, COUNT(DISTINCT ds.user_id) AS user_count,
		       ARRAY_AGG(DISTINCT u.phone_number) AS phones
		FROM device_sessions ds
		JOIN users u ON u.id = ds.user_id
		GROUP BY ds.device_id
		HAVING COUNT(DISTINCT ds.user_id) > 1
		ORDER BY user_count DESC
		LIMIT 100
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

// ListRides returns all rides with full detail for admin.
func (s *Service) ListRides(ctx context.Context, limit, offset int) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.status, r.transport_type, r.customer_id,
		       cu.phone_number AS customer_phone,
		       r.driver_id, du.phone_number AS driver_phone,
		       r.agreed_fare, r.created_at
		FROM rides r
		JOIN users cu ON cu.id = r.customer_id
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		LEFT JOIN users du ON du.id = dp.user_id
		ORDER BY r.created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, status, transportType, customerID, customerPhone string
		var driverID, driverPhone *string
		var agreedFare *float64
		var createdAt time.Time
		if err := rows.Scan(&id, &status, &transportType, &customerID, &customerPhone, &driverID, &driverPhone, &agreedFare, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "status": status, "transport_type": transportType,
			"customer_id": customerID, "customer_phone": customerPhone,
			"driver_id": driverID, "driver_phone": driverPhone,
			"agreed_fare": agreedFare, "created_at": createdAt,
		})
	}
	return result, nil
}
