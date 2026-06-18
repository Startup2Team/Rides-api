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
