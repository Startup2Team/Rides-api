package admin

import (
	"context"
	"errors"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

	"github.com/jackc/pgx/v5"
)

// Admin trust & safety: GPS anomalies, device collisions, lockout/flag
// clearing and the per-account moderation timeline.

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
