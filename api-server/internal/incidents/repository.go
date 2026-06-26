package incidents

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

func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Incident, int, error) {
	where, args := buildWhere(f)
	countQ := `SELECT COUNT(*) FROM safety_incidents i ` + where
	var total int
	if err := r.db.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitOffset := len(args) + 1
	q := fmt.Sprintf(`
		SELECT i.id, i.type, i.severity, i.status, i.description, i.ride_id,
		       i.reporter_user_id, u.full_name, u.phone_number, i.reporter_role,
		       i.location_text, i.district, i.notes, i.reported_at, i.updated_at
		FROM safety_incidents i
		LEFT JOIN users u ON u.id = i.reporter_user_id
		%s ORDER BY i.reported_at DESC LIMIT $%d OFFSET $%d
	`, where, limitOffset, limitOffset+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*Incident
	for rows.Next() {
		inc := &Incident{}
		if err := rows.Scan(
			&inc.ID, &inc.Type, &inc.Severity, &inc.Status, &inc.Description,
			&inc.RideID, &inc.ReporterUserID, &inc.ReporterName, &inc.ReporterPhone,
			&inc.ReporterRole, &inc.LocationText, &inc.District, &inc.Notes,
			&inc.ReportedAt, &inc.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		result = append(result, inc)
	}
	return result, total, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (*Incident, error) {
	inc := &Incident{}
	err := r.db.QueryRow(ctx, `
		SELECT i.id, i.type, i.severity, i.status, i.description, i.ride_id,
		       i.reporter_user_id, u.full_name, u.phone_number, i.reporter_role,
		       i.location_text, i.district, i.notes, i.reported_at, i.updated_at
		FROM safety_incidents i
		LEFT JOIN users u ON u.id = i.reporter_user_id
		WHERE i.id = $1
	`, id).Scan(
		&inc.ID, &inc.Type, &inc.Severity, &inc.Status, &inc.Description,
		&inc.RideID, &inc.ReporterUserID, &inc.ReporterName, &inc.ReporterPhone,
		&inc.ReporterRole, &inc.LocationText, &inc.District, &inc.Notes,
		&inc.ReportedAt, &inc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	events, err := r.listEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	inc.Timeline = events
	return inc, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE safety_incidents SET status = $1, updated_at = NOW() WHERE id = $2`,
		status, id)
	return err
}

func (r *Repository) AppendEvent(ctx context.Context, incidentID, text, kind string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO incident_events (incident_id, event_text, kind) VALUES ($1, $2, $3)`,
		incidentID, text, kind)
	return err
}

func (r *Repository) UpdateNotes(ctx context.Context, id, notes string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE safety_incidents SET notes = $1, updated_at = NOW() WHERE id = $2`,
		notes, id)
	return err
}

func (r *Repository) Create(ctx context.Context, incType, severity, description, reporterRole, locationText, district string, rideID, reporterUserID *string) (*Incident, error) {
	inc := &Incident{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO safety_incidents
		  (type, severity, description, reporter_role, location_text, district, ride_id, reporter_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, type, severity, status, description, ride_id,
		          reporter_user_id, NULL, NULL, reporter_role,
		          location_text, district, notes, reported_at, updated_at
	`, incType, severity, description, reporterRole, locationText, district, rideID, reporterUserID,
	).Scan(
		&inc.ID, &inc.Type, &inc.Severity, &inc.Status, &inc.Description,
		&inc.RideID, &inc.ReporterUserID, &inc.ReporterName, &inc.ReporterPhone,
		&inc.ReporterRole, &inc.LocationText, &inc.District, &inc.Notes,
		&inc.ReportedAt, &inc.UpdatedAt,
	)
	return inc, err
}

func (r *Repository) listEvents(ctx context.Context, incidentID string) ([]IncidentEvent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, incident_id, event_text, kind, created_at
		 FROM incident_events WHERE incident_id = $1 ORDER BY created_at ASC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []IncidentEvent
	for rows.Next() {
		ev := IncidentEvent{}
		if err := rows.Scan(&ev.ID, &ev.IncidentID, &ev.EventText, &ev.Kind, &ev.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func buildWhere(f ListFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	n := 1

	if f.Status != "" && f.Status != "All" {
		clauses = append(clauses, fmt.Sprintf("i.status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.Severity != "" && f.Severity != "All" {
		clauses = append(clauses, fmt.Sprintf("i.severity = $%d", n))
		args = append(args, f.Severity)
		n++
	}
	if f.Type != "" && f.Type != "All" {
		clauses = append(clauses, fmt.Sprintf("i.type = $%d", n))
		args = append(args, f.Type)
		n++
	}
	if f.Search != "" {
		clauses = append(clauses, fmt.Sprintf("(i.description ILIKE $%d OR i.location_text ILIKE $%d)", n, n))
		args = append(args, "%"+f.Search+"%")
		n++
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (r *Repository) Stats(ctx context.Context) (map[string]interface{}, error) {
	var open, acknowledged, escalated, resolved7d int
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM safety_incidents WHERE status='OPEN'`).Scan(&open)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM safety_incidents WHERE status='ACKNOWLEDGED'`).Scan(&acknowledged)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM safety_incidents WHERE status='ESCALATED'`).Scan(&escalated)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM safety_incidents WHERE status='RESOLVED' AND updated_at >= NOW()-INTERVAL '7 days'`).Scan(&resolved7d)
	return map[string]interface{}{
		"open": open, "acknowledged": acknowledged, "escalated": escalated, "resolved_7d": resolved7d,
	}, nil
}
