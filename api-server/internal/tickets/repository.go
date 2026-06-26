package tickets

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Ticket, int, error) {
	where, args := buildWhere(f)
	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets t `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	n := len(args) + 1
	q := fmt.Sprintf(`
		SELECT t.id, t.subject, t.type, t.priority, t.status,
		       t.from_user_id, u.full_name, u.phone_number, t.from_role,
		       t.ride_id, t.assigned_to, t.created_at, t.updated_at
		FROM support_tickets t
		LEFT JOIN users u ON u.id = t.from_user_id
		%s ORDER BY t.created_at DESC LIMIT $%d OFFSET $%d
	`, where, n, n+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*Ticket
	for rows.Next() {
		t := &Ticket{}
		if err := rows.Scan(
			&t.ID, &t.Subject, &t.Type, &t.Priority, &t.Status,
			&t.FromUserID, &t.FromName, &t.FromPhone, &t.FromRole,
			&t.RideID, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		result = append(result, t)
	}
	return result, total, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	err := r.db.QueryRow(ctx, `
		SELECT t.id, t.subject, t.type, t.priority, t.status,
		       t.from_user_id, u.full_name, u.phone_number, t.from_role,
		       t.ride_id, t.assigned_to, t.created_at, t.updated_at
		FROM support_tickets t
		LEFT JOIN users u ON u.id = t.from_user_id
		WHERE t.id = $1
	`, id).Scan(
		&t.ID, &t.Subject, &t.Type, &t.Priority, &t.Status,
		&t.FromUserID, &t.FromName, &t.FromPhone, &t.FromRole,
		&t.RideID, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	msgs, err := r.listMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Messages = msgs
	return t, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE support_tickets SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

func (r *Repository) Assign(ctx context.Context, id, adminID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE support_tickets SET assigned_to = $1, status = 'PENDING', updated_at = NOW() WHERE id = $2`, adminID, id)
	return err
}

func (r *Repository) AddMessage(ctx context.Context, ticketID, fromRole, author, body string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO ticket_messages (ticket_id, from_role, author, body) VALUES ($1,$2,$3,$4)`,
		ticketID, fromRole, author, body)
	return err
}

func (r *Repository) Create(ctx context.Context, subject, ticketType, priority, fromRole string, fromUserID, rideID *string) (*Ticket, error) {
	t := &Ticket{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO support_tickets (subject, type, priority, from_role, from_user_id, ride_id)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, subject, type, priority, status,
		          from_user_id, NULL, NULL, from_role,
		          ride_id, assigned_to, created_at, updated_at
	`, subject, ticketType, priority, fromRole, fromUserID, rideID).Scan(
		&t.ID, &t.Subject, &t.Type, &t.Priority, &t.Status,
		&t.FromUserID, &t.FromName, &t.FromPhone, &t.FromRole,
		&t.RideID, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

func (r *Repository) listMessages(ctx context.Context, ticketID string) ([]TicketMessage, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, ticket_id, from_role, author, body, created_at
		 FROM ticket_messages WHERE ticket_id = $1 ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []TicketMessage
	for rows.Next() {
		m := TicketMessage{}
		if err := rows.Scan(&m.ID, &m.TicketID, &m.FromRole, &m.Author, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func buildWhere(f ListFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	n := 1

	if f.Status != "" {
		clauses = append(clauses, fmt.Sprintf("t.status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.Priority != "" {
		clauses = append(clauses, fmt.Sprintf("t.priority = $%d", n))
		args = append(args, f.Priority)
		n++
	}
	if f.Type != "" {
		clauses = append(clauses, fmt.Sprintf("t.type = $%d", n))
		args = append(args, f.Type)
		n++
	}
	if f.Search != "" {
		clauses = append(clauses, fmt.Sprintf("t.subject ILIKE $%d", n))
		args = append(args, "%"+f.Search+"%")
		n++
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (r *Repository) Stats(ctx context.Context) (map[string]interface{}, error) {
	var open, pending, resolvedToday int
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE status='OPEN'`).Scan(&open)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE status='PENDING'`).Scan(&pending)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE status='RESOLVED' AND updated_at>=CURRENT_DATE`).Scan(&resolvedToday)
	return map[string]interface{}{
		"open": open, "pending": pending, "resolved_today": resolvedToday,
	}, nil
}
