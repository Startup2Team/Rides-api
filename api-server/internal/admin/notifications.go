package admin

import (
	"context"
	"net/http"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// CreateNotificationCampaign records an admin notification campaign and delivers
// it to the target audience. Delivery is a REAL push: for each target user it
// persists an in-app feed row AND pushes to every registered device (via the
// notifier). Delivery runs in the background so the admin request returns
// immediately; the campaign row is the record of intent. When no notifier is
// wired (e.g. tests), it falls back to a feed-only set-based insert.
func (s *Service) CreateNotificationCampaign(ctx context.Context, title, body, audience, createdBy string) (map[string]interface{}, error) {
	if title == "" || body == "" || audience == "" {
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "title, body, and audience are required")
	}

	switch audience {
	case "ALL", "DRIVERS", "CUSTOMERS":
		// valid
	default:
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_AUDIENCE", "audience must be ALL, DRIVERS, or CUSTOMERS")
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
		// Real push (feed + all devices), in the background. Detached context:
		// the admin request returns before delivery finishes.
		go s.deliverCampaignPush(campaignID, title, body, audience)
	} else if err := s.deliverCampaignFeedOnly(ctx, title, body, audience); err != nil {
		// No push wired — at least populate the in-app feed. Non-fatal: the
		// campaign is already recorded.
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

// deliverCampaignPush fans the campaign out to every target user: an in-app feed
// row plus a push to each of their devices. Runs in its own goroutine with a
// detached, time-bounded context. Best-effort per user (SendToAllDevices logs
// and prunes dead tokens internally).
// NOTE (scale): this is a per-user loop — fine for now; move to a queue/batch
// worker when audiences get large or delivery must survive a process restart.
func (s *Service) deliverCampaignPush(campaignID, title, body, audience string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ids, err := s.targetUserIDs(ctx, audience)
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

// targetUserIDs resolves the user IDs for an audience (validated by the caller).
func (s *Service) targetUserIDs(ctx context.Context, audience string) ([]string, error) {
	var q string
	switch audience {
	case "DRIVERS":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case "CUSTOMERS":
		q = `SELECT id FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	default: // ALL
		q = `SELECT id FROM users WHERE is_suspended = FALSE`
	}
	rows, err := s.db.Query(ctx, q)
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

// deliverCampaignFeedOnly writes in-app feed rows (no push) with one set-based
// insert. Fallback used only when no notifier is wired.
func (s *Service) deliverCampaignFeedOnly(ctx context.Context, title, body, audience string) error {
	var q string
	switch audience {
	case "DRIVERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, 'promo', '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case "CUSTOMERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, 'promo', '{}'::jsonb FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	default: // ALL
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, 'promo', '{}'::jsonb FROM users WHERE is_suspended = FALSE`
	}
	_, err := s.db.Exec(ctx, q, title, body)
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
