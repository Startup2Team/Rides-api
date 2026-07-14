package notification

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Notification is a persisted user notification.
type Notification struct {
	ID     string            `json:"id"`
	UserID string            `json:"user_id"`
	Title  string            `json:"title"`
	Body   string            `json:"body"`
	Type   string            `json:"type"`
	Data   map[string]string `json:"data,omitempty"`
	IsRead bool              `json:"is_read"`
	SentAt time.Time         `json:"sent_at"`
	ReadAt *time.Time        `json:"read_at,omitempty"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, userID, title, body, nType string, data map[string]string) (*Notification, error) {
	n := &Notification{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO notifications (user_id, title, body, type, data, is_read, sent_at)
		VALUES ($1, $2, $3, $4, $5, FALSE, NOW())
		RETURNING id, user_id, title, body, type, data, is_read, sent_at, read_at
	`, userID, title, body, nType, data).Scan(
		&n.ID, &n.UserID, &n.Title, &n.Body, &n.Type, &n.Data, &n.IsRead, &n.SentAt, &n.ReadAt,
	)
	return n, err
}

func (r *Repository) ListByUser(ctx context.Context, userID string, limit, offset int) ([]*Notification, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, title, body, type, data, is_read, sent_at, read_at
		FROM notifications
		WHERE user_id = $1
		ORDER BY sent_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notifs []*Notification
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Body, &n.Type, &n.Data, &n.IsRead, &n.SentAt, &n.ReadAt); err != nil {
			return nil, err
		}
		notifs = append(notifs, n)
	}
	return notifs, rows.Err()
}

func (r *Repository) UnreadCount(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND is_read = FALSE
	`, userID).Scan(&count)
	return count, err
}

func (r *Repository) MarkRead(ctx context.Context, notifID, userID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE notifications SET is_read = TRUE, read_at = NOW()
		WHERE id = $1 AND user_id = $2
	`, notifID, userID)
	return err
}

func (r *Repository) MarkAllRead(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE notifications SET is_read = TRUE, read_at = NOW()
		WHERE user_id = $1 AND is_read = FALSE
	`, userID)
	return err
}

// ── Device tokens (multi-device FCM) ─────────────────────────────────────────

// UpsertDeviceToken registers (or refreshes) an FCM token for a user. A token
// can only belong to one account at a time, so any prior owner is cleared first;
// then it is upserted for this user and mirrored to the legacy users.fcm_token
// column so the matching engine / expiry notifier keep working.
func (r *Repository) UpsertDeviceToken(ctx context.Context, userID, token, platform string) error {
	if platform == "" {
		platform = "unknown"
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Reassign the token from any other user to this one.
	if _, err := tx.Exec(ctx, `DELETE FROM device_tokens WHERE token = $1 AND user_id <> $2`, token, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO device_tokens (user_id, token, platform, last_seen, updated_at)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (user_id, token)
		DO UPDATE SET platform = EXCLUDED.platform, last_seen = NOW(), updated_at = NOW()
	`, userID, token, platform); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET fcm_token = $1, updated_at = NOW() WHERE id = $2`, token, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListDeviceTokens returns every FCM token registered for a user, unioned with
// the legacy users.fcm_token so users who registered before this table existed
// (or only via a profile update) still receive pushes.
func (r *Repository) ListDeviceTokens(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT token FROM device_tokens WHERE user_id = $1
		UNION
		SELECT fcm_token FROM users WHERE id = $1 AND fcm_token IS NOT NULL AND fcm_token <> ''
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DeleteDeviceToken removes a token for a user (explicit unregister, e.g. logout
// on that device).
func (r *Repository) DeleteDeviceToken(ctx context.Context, userID, token string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM device_tokens WHERE user_id = $1 AND token = $2`, userID, token)
	if err != nil {
		return err
	}
	// Clear the legacy mirror if it pointed at this token.
	_, err = r.db.Exec(ctx, `UPDATE users SET fcm_token = NULL WHERE id = $1 AND fcm_token = $2`, userID, token)
	return err
}

// PruneDeviceToken removes a dead token everywhere (FCM said it's unregistered).
func (r *Repository) PruneDeviceToken(ctx context.Context, token string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM device_tokens WHERE token = $1`, token); err != nil {
		return err
	}
	_, err := r.db.Exec(ctx, `UPDATE users SET fcm_token = NULL WHERE fcm_token = $1`, token)
	return err
}
