package admin

import (
	"context"
	"net/http"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// CreateNotificationCampaign creates an admin notification campaign and delivers it to the target users' notification feeds in a transactional block.
func (s *Service) CreateNotificationCampaign(ctx context.Context, title, body, audience, createdBy string) (map[string]interface{}, error) {
	if title == "" || body == "" || audience == "" {
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "title, body, and audience are required")
	}

	// Validate audience type
	switch audience {
	case "ALL", "DRIVERS", "CUSTOMERS":
		// valid
	default:
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_AUDIENCE", "audience must be ALL, DRIVERS, or CUSTOMERS")
	}

	// Begin transactional block
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 1. Insert campaign record
	var campaignID string
	var status string
	var sentAt, createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO admin_notifications (title, body, audience, status, created_by)
		VALUES ($1, $2, $3, 'SENT', $4)
		RETURNING id, status, sent_at, created_at
	`, title, body, audience, createdBy).Scan(&campaignID, &status, &sentAt, &createdAt)
	if err != nil {
		return nil, err
	}

	// 2. Deliver to target users using set-based bulk insertion
	var deliverQuery string
	switch audience {
	case "ALL":
		deliverQuery = `
			INSERT INTO notifications (user_id, title, body, type, data)
			SELECT id, $1, $2, 'promo', '{}'::jsonb
			FROM users WHERE is_suspended = FALSE
		`
	case "DRIVERS":
		deliverQuery = `
			INSERT INTO notifications (user_id, title, body, type, data)
			SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
			FROM driver_profiles dp
			JOIN users u ON u.id = dp.user_id
			WHERE u.is_suspended = FALSE
		`
	case "CUSTOMERS":
		deliverQuery = `
			INSERT INTO notifications (user_id, title, body, type, data)
			SELECT id, $1, $2, 'promo', '{}'::jsonb
			FROM users
			WHERE is_suspended = FALSE
			  AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)
		`
	}

	_, err = tx.Exec(ctx, deliverQuery, title, body)
	if err != nil {
		return nil, err
	}

	// Commit transactional block
	if err := tx.Commit(ctx); err != nil {
		return nil, err
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
