package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Message, int, error) {
	where, args := buildWhere(f)
	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM inbox_messages `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	n := len(args) + 1
	q := fmt.Sprintf(`
		SELECT id, from_name, from_email, category, status, subject, body,
		       reply_body, replied_at, created_at, updated_at
		FROM inbox_messages %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d
	`, where, n, n+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(
			&m.ID, &m.FromName, &m.FromEmail, &m.Category, &m.Status,
			&m.Subject, &m.Body, &m.ReplyBody, &m.RepliedAt,
			&m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		result = append(result, m)
	}
	return result, total, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (*Message, error) {
	m := &Message{}
	err := r.db.QueryRow(ctx, `
		SELECT id, from_name, from_email, category, status, subject, body,
		       reply_body, replied_at, created_at, updated_at
		FROM inbox_messages WHERE id = $1
	`, id).Scan(
		&m.ID, &m.FromName, &m.FromEmail, &m.Category, &m.Status,
		&m.Subject, &m.Body, &m.ReplyBody, &m.RepliedAt,
		&m.CreatedAt, &m.UpdatedAt,
	)
	return m, err
}

func (r *Repository) Reply(ctx context.Context, id, replyBody string) error {
	now := time.Now()
	_, err := r.db.Exec(ctx,
		`UPDATE inbox_messages SET reply_body=$1, replied_at=$2, status='REPLIED', updated_at=NOW() WHERE id=$3`,
		replyBody, now, id)
	return err
}

func (r *Repository) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE inbox_messages SET status=$1, updated_at=NOW() WHERE id=$2`, status, id)
	return err
}

func buildWhere(f ListFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	n := 1

	if f.Status != "" && f.Status != "All" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.Category != "" {
		clauses = append(clauses, fmt.Sprintf("category = $%d", n))
		args = append(args, f.Category)
		n++
	}
	if f.Search != "" {
		clauses = append(clauses, fmt.Sprintf("(subject ILIKE $%d OR from_name ILIKE $%d OR from_email ILIKE $%d)", n, n, n))
		args = append(args, "%"+f.Search+"%")
		n++
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
