package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// CreateNotificationCampaign records an admin notification campaign and delivers
// it to the target audience. Delivery is a REAL push: for each target user it
// persists an in-app feed row AND pushes to every registered device (via the
// notifier).
func (s *Service) CreateNotificationCampaign(ctx context.Context, title, body, audience, createdBy string, targetDriverID ...string) (map[string]interface{}, error) {
	if title == "" || body == "" || audience == "" {
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "title, body, and audience are required")
	}

	tid := ""
	if len(targetDriverID) > 0 {
		tid = targetDriverID[0]
	}

	switch audience {
	case "ALL", "DRIVERS", "CUSTOMERS", "DRIVER_MOTO", "DRIVER_CAB", "DRIVER_HILUX", "DRIVER_FUSO", "DRIVER_RIFANI", "SINGLE_DRIVER":
		// valid
	default:
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_AUDIENCE", "audience must be ALL, DRIVERS, CUSTOMERS, DRIVER_MOTO, DRIVER_CAB, DRIVER_HILUX, DRIVER_FUSO, DRIVER_RIFANI, or SINGLE_DRIVER")
	}

	if audience == "SINGLE_DRIVER" && tid == "" {
		return nil, apperrors.New(http.StatusBadRequest, "TARGET_DRIVER_REQUIRED", "target driver ID is required for SINGLE_DRIVER audience")
	}

	var campaignID, status string
	var sentAt, createdAt time.Time
	err := s.db.QueryRow(ctx, `
		INSERT INTO admin_notifications (title, body, audience, status, created_by)
		VALUES ($1, $2, $3, 'SENT', $4)
		RETURNING id, status, sent_at, created_at
	`, title, body, audience, createdBy).Scan(&campaignID, &status, &sentAt, &createdAt)
	if err != nil {
		return nil, err
	}

	if s.notifier != nil {
		go s.deliverCampaignPush(campaignID, title, body, audience, tid)
	} else if err := s.deliverCampaignFeedOnly(ctx, title, body, audience, tid); err != nil {
		s.log.Warn().Err(err).Str("campaign_id", campaignID).Msg("notifications: feed-only delivery failed")
	}

	return map[string]interface{}{
		"id":         campaignID,
		"title":      title,
		"body":       body,
		"audience":   audience,
		"status":     status,
		"sent_at":    sentAt,
		"created_by": createdBy,
		"created_at": createdAt,
	}, nil
}

// NotifyDriver sends a direct targeted message / push notification to a specific driver.
func (s *Service) NotifyDriver(ctx context.Context, driverIDOrUserID, title, body, reason, createdBy string) (map[string]interface{}, error) {
	if title == "" || body == "" || driverIDOrUserID == "" {
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "driver_id, title, and body are required")
	}

	var userID, fullName, phone, transportType string
	err := s.db.QueryRow(ctx, `
		SELECT dp.user_id, COALESCE(u.full_name, ''), COALESCE(u.phone, ''), COALESCE(dp.transport_type, '')
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		WHERE dp.id = $1 OR dp.user_id = $1 OR u.id = $1
		LIMIT 1
	`, driverIDOrUserID).Scan(&userID, &fullName, &phone, &transportType)
	if err != nil {
		return nil, apperrors.New(http.StatusNotFound, "DRIVER_NOT_FOUND", "driver profile or user not found")
	}

	aud := "SINGLE_DRIVER"
	var campaignID, status string
	var sentAt, createdAt time.Time
	err = s.db.QueryRow(ctx, `
		INSERT INTO admin_notifications (title, body, audience, status, created_by)
		VALUES ($1, $2, $3, 'SENT', $4)
		RETURNING id, status, sent_at, created_at
	`, title, body, aud, createdBy).Scan(&campaignID, &status, &sentAt, &createdAt)
	if err != nil {
		return nil, err
	}

	data := map[string]string{
		"kind":        "direct_driver_notice",
		"reason":      reason,
		"campaign_id": campaignID,
	}

	if s.notifier != nil {
		s.notifier.SendToAllDevices(ctx, userID, title, body, "notice", data)
	} else {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO notifications (user_id, title, body, type, data)
			VALUES ($1, $2, $3, 'notice', $4::jsonb)
		`, userID, title, body, fmt.Sprintf(`{"reason":"%s"}`, reason))
	}

	return map[string]interface{}{
		"id":             campaignID,
		"user_id":        userID,
		"driver_id":      driverIDOrUserID,
		"driver_name":    fullName,
		"driver_phone":   phone,
		"transport_type": transportType,
		"title":          title,
		"body":           body,
		"reason":         reason,
		"audience":       aud,
		"status":         status,
		"sent_at":        sentAt,
		"created_by":     createdBy,
		"created_at":     createdAt,
	}, nil
}

func (s *Service) deliverCampaignPush(campaignID, title, body, audience, targetDriverID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ids, err := s.targetUserIDs(ctx, audience, targetDriverID)
	if err != nil {
		s.log.Error().Err(err).Str("campaign_id", campaignID).Msg("notifications: resolve audience failed")
		return
	}
	s.log.Info().Str("campaign_id", campaignID).Str("audience", audience).Int("recipients", len(ids)).Msg("notifications: campaign delivery started")

	data := map[string]string{"kind": "campaign", "campaign_id": campaignID}
	for _, uid := range ids {
		s.notifier.SendToAllDevices(ctx, uid, title, body, "promo", data)
	}
	s.log.Info().Str("campaign_id", campaignID).Int("delivered", len(ids)).Msg("notifications: campaign delivery finished")
}

func (s *Service) targetUserIDs(ctx context.Context, audience, targetDriverID string) ([]string, error) {
	var q string
	var args []interface{}
	switch audience {
	case "DRIVERS":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case "DRIVER_MOTO":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'MOTO_BIKE'`
	case "DRIVER_CAB":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'CAB_TAXI'`
	case "DRIVER_HILUX":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'LIGHT_HILUX'`
	case "DRIVER_FUSO":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'HEAVY_FUSO'`
	case "DRIVER_RIFANI":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'RIFANI'`
	case "SINGLE_DRIVER":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		      WHERE dp.id = $1 OR dp.user_id = $1`
		args = append(args, targetDriverID)
	case "CUSTOMERS":
		q = `SELECT id FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	default: // ALL
		q = `SELECT id FROM users WHERE is_suspended = FALSE`
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) deliverCampaignFeedOnly(ctx context.Context, title, body, audience, targetDriverID string) error {
	var q string
	var args []interface{}
	args = append(args, title, body)

	switch audience {
	case "DRIVERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case "DRIVER_MOTO":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'MOTO_BIKE'`
	case "DRIVER_CAB":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'CAB_TAXI'`
	case "DRIVER_HILUX":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'LIGHT_HILUX'`
	case "DRIVER_FUSO":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'HEAVY_FUSO'`
	case "DRIVER_RIFANI":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = 'RIFANI'`
	case "SINGLE_DRIVER":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'notice', '{}'::jsonb
		       FROM driver_profiles dp WHERE dp.id = $3 OR dp.user_id = $3`
		args = append(args, targetDriverID)
	case "CUSTOMERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, 'promo', '{}'::jsonb FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	default: // ALL
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, 'promo', '{}'::jsonb FROM users WHERE is_suspended = FALSE`
	}
	_, err := s.db.Exec(ctx, q, args...)
	return err
}

// ListNotificationCampaigns lists past admin notification campaigns.
func (s *Service) ListNotificationCampaigns(ctx context.Context, limit, offset int) ([]map[string]interface{}, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var total int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_notifications`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, title, body, audience, status, sent_at, COALESCE(created_by, ''), created_at
		FROM admin_notifications
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var campaigns []map[string]interface{}
	for rows.Next() {
		var id, title, body, audience, status, createdBy string
		var sentAt, createdAt time.Time
		if err := rows.Scan(&id, &title, &body, &audience, &status, &sentAt, &createdBy, &createdAt); err != nil {
			return nil, 0, err
		}
		campaigns = append(campaigns, map[string]interface{}{
			"id":         id,
			"title":      title,
			"body":       body,
			"audience":   audience,
			"status":     status,
			"sent_at":    sentAt,
			"created_by": createdBy,
			"created_at": createdAt,
		})
	}

	return campaigns, total, nil
}

// DeleteNotificationCampaign deletes a campaign record.
func (s *Service) DeleteNotificationCampaign(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_notifications WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}
