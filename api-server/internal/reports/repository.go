package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) List(ctx context.Context, status, format string, limit, offset int) ([]*Report, int, error) {
	base := `FROM reports`
	var args []interface{}
	n := 1

	where := ""
	if status != "" {
		where = ` WHERE status = $1`
		args = append(args, status)
		n++
	}
	if format != "" {
		if where == "" {
			where = ` WHERE format = $1`
		} else {
			where += ` AND format = $` + itoa(n)
		}
		args = append(args, format)
		n++
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) `+base+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := r.db.Query(ctx,
		`SELECT id, template, status, format, date_range, file_size, file_path, generated_at, created_by, created_at `+
			base+where+` ORDER BY created_at DESC LIMIT $`+itoa(n)+` OFFSET $`+itoa(n+1),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []*Report
	for rows.Next() {
		rep := &Report{}
		if err := rows.Scan(&rep.ID, &rep.Template, &rep.Status, &rep.Format,
			&rep.DateRange, &rep.FileSize, &rep.FilePath, &rep.GeneratedAt, &rep.CreatedBy, &rep.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, rep)
	}
	return result, total, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (*Report, error) {
	rep := &Report{}
	err := r.db.QueryRow(ctx, `
		SELECT id, template, status, format, date_range, file_size, file_path, generated_at, created_by, created_at
		FROM reports WHERE id = $1
	`, id).Scan(&rep.ID, &rep.Template, &rep.Status, &rep.Format,
		&rep.DateRange, &rep.FileSize, &rep.FilePath, &rep.GeneratedAt, &rep.CreatedBy, &rep.CreatedAt)
	return rep, err
}

func (r *Repository) Create(ctx context.Context, template, format, dateRange string, createdBy *string) (*Report, error) {
	rep := &Report{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO reports (template, format, date_range, created_by)
		VALUES ($1,$2,$3,$4)
		RETURNING id, template, status, format, date_range, file_size, file_path, generated_at, created_by, created_at
	`, template, format, dateRange, createdBy).Scan(
		&rep.ID, &rep.Template, &rep.Status, &rep.Format,
		&rep.DateRange, &rep.FileSize, &rep.FilePath, &rep.GeneratedAt, &rep.CreatedBy, &rep.CreatedAt)
	return rep, err
}

func (r *Repository) MarkReady(ctx context.Context, id, filePath, fileSize string) error {
	now := time.Now()
	_, err := r.db.Exec(ctx,
		`UPDATE reports SET status='READY', file_path=$1, file_size=$2, generated_at=$3 WHERE id=$4`,
		filePath, fileSize, now, id)
	return err
}

func (r *Repository) MarkFailed(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE reports SET status='FAILED' WHERE id=$1`, id)
	return err
}

func (r *Repository) ListScheduled(ctx context.Context) ([]*ScheduledReport, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, template, format, frequency, recipients, is_active, next_run, created_at
		FROM scheduled_reports ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*ScheduledReport
	for rows.Next() {
		sr := &ScheduledReport{}
		if err := rows.Scan(&sr.ID, &sr.Template, &sr.Format, &sr.Frequency,
			&sr.Recipients, &sr.IsActive, &sr.NextRun, &sr.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, sr)
	}
	return result, nil
}

func (r *Repository) CreateScheduled(ctx context.Context, template, format, frequency string, recipients []string) (*ScheduledReport, error) {
	sr := &ScheduledReport{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO scheduled_reports (template, format, frequency, recipients, next_run)
		VALUES ($1,$2,$3,$4, NOW() + INTERVAL '1 day')
		RETURNING id, template, format, frequency, recipients, is_active, next_run, created_at
	`, template, format, frequency, recipients).Scan(
		&sr.ID, &sr.Template, &sr.Format, &sr.Frequency,
		&sr.Recipients, &sr.IsActive, &sr.NextRun, &sr.CreatedAt)
	return sr, err
}

func (r *Repository) ToggleScheduled(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE scheduled_reports SET is_active = NOT is_active WHERE id=$1`, id)
	return err
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func (r *Repository) Stats(ctx context.Context) (map[string]interface{}, error) {
	var totalMonth, readyWeek, scheduled, pending int
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE created_at >= DATE_TRUNC('month', NOW())`).Scan(&totalMonth)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE status = 'READY' AND created_at >= NOW() - INTERVAL '7 days'`).Scan(&readyWeek)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM scheduled_reports WHERE is_active = TRUE`).Scan(&scheduled)
	_ = r.db.QueryRow(ctx, `SELECT COUNT(*) FROM reports WHERE status = 'PENDING'`).Scan(&pending)
	return map[string]interface{}{
		"total_this_month": totalMonth,
		"ready_this_week":  readyWeek,
		"scheduled":        scheduled,
		"pending":          pending,
	}, nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM reports WHERE id = $1`, id)
	return err
}

func (r *Repository) GetFilePath(ctx context.Context, id string) (string, error) {
	var filePath *string
	err := r.db.QueryRow(ctx, `SELECT file_path FROM reports WHERE id = $1`, id).Scan(&filePath)
	if err != nil {
		return "", err
	}
	if filePath == nil || *filePath == "" {
		return "", fmt.Errorf("no file")
	}
	return *filePath, nil
}
