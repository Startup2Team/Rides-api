// Package audit provides a thin helper for writing immutable admin audit entries.
// Errors are always silenced — audit failures must never abort a business operation.
package audit

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the minimal interface required to write audit entries.
// *pgxpool.Pool satisfies it automatically.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Logger writes to admin_audit_log. Obtain one via New and inject it into handlers.
type Logger struct {
	db DB
}

func New(db DB) *Logger {
	return &Logger{db: db}
}

// Record inserts one audit entry. It never returns an error — callers do not need
// to handle audit failures.
func (l *Logger) Record(ctx context.Context, adminID, adminRole, action, targetType, targetID, summary string, metadata map[string]any) {
	l.write(ctx, adminID, adminRole, action, targetType, targetID, summary, "", metadata)
}

// RecordIP is like Record but also captures the originating request IP.
func (l *Logger) RecordIP(ctx context.Context, adminID, adminRole, action, targetType, targetID, summary, ip string, metadata map[string]any) {
	l.write(ctx, adminID, adminRole, action, targetType, targetID, summary, ip, metadata)
}

func (l *Logger) write(ctx context.Context, adminID, adminRole, action, targetType, targetID, summary, ip string, metadata map[string]any) {
	var metaJSON *string
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			s := string(b)
			metaJSON = &s
		}
	}
	_, _ = l.db.Exec(ctx, `
		INSERT INTO admin_audit_log
		    (admin_id, admin_role, action, target_type, target_id, detail, ip, metadata)
		VALUES
		    ($1, NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8::jsonb)
	`, adminID, adminRole, action, targetType, targetID, summary, ip, metaJSON)
}
