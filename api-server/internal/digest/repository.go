// Package digest builds the daily operations summary that is pushed to the
// team's Telegram group each morning, and backs the on-demand /stats command.
//
// The point is to make silence meaningful. Before this, Telegram only carried
// deploy notices, so "no messages" was indistinguishable from "nothing is
// working" — which is precisely how every document upload could fail for weeks
// against a revoked storage credential without anyone noticing.
package digest

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is one morning's numbers: yesterday's activity, the day before for
// comparison, and the current state of anything a human may need to act on.
type Snapshot struct {
	Day      time.Time // the local day being reported (yesterday)
	Location string

	// Rides
	RidesCompleted     int
	RidesCancelled     int
	RidesRequested     int
	RidesCompletedPrev int
	FareRWF            int64
	FarePrevRWF        int64

	// Growth
	NewCustomers int
	NewDrivers   int
	TotalUsers   int

	// Drivers — the actionable column
	PendingApplications int
	ApprovedDrivers     int
	OnlineNow           int
	ExpiringDocuments   int // licence/insurance/authorisation expiring within 30 days

	// Money
	PackagesSold    int
	PackageRevenue  int64
	PaymentsByState map[string]int

	// Support load
	OpenTickets   int
	OpenIncidents int

	// Platform health
	DocumentsUploaded int
	StorageOK         bool
	StorageErr        string
	DBSize            string
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// Collect gathers every figure for the local day containing `day`.
//
// Queries deliberately key off nullable timestamps (completed_at, paid_at)
// rather than status string literals wherever the timestamp carries the same
// meaning — status vocabularies drift per domain, a timestamp does not.
func (r *Repository) Collect(ctx context.Context, day time.Time, loc *time.Location) (*Snapshot, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	prevStart := start.AddDate(0, 0, -1)

	s := &Snapshot{Day: start, Location: loc.String(), PaymentsByState: map[string]int{}}

	// ── Rides ────────────────────────────────────────────────────────────────
	if err := r.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE completed_at >= $1 AND completed_at < $2),
		  count(*) FILTER (WHERE status = 'CANCELLED' AND updated_at >= $1 AND updated_at < $2),
		  count(*) FILTER (WHERE created_at   >= $1 AND created_at   < $2),
		  count(*) FILTER (WHERE completed_at >= $3 AND completed_at < $1),
		  COALESCE(sum(COALESCE(final_fare_rwf, agreed_fare, 0))
		           FILTER (WHERE completed_at >= $1 AND completed_at < $2), 0)::bigint,
		  COALESCE(sum(COALESCE(final_fare_rwf, agreed_fare, 0))
		           FILTER (WHERE completed_at >= $3 AND completed_at < $1), 0)::bigint
		FROM rides
	`, start, end, prevStart).Scan(
		&s.RidesCompleted, &s.RidesCancelled, &s.RidesRequested,
		&s.RidesCompletedPrev, &s.FareRWF, &s.FarePrevRWF,
	); err != nil {
		return nil, err
	}

	// ── Growth ───────────────────────────────────────────────────────────────
	if err := r.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE created_at >= $1 AND created_at < $2),
		  count(*)
		FROM users
	`, start, end).Scan(&s.NewCustomers, &s.TotalUsers); err != nil {
		return nil, err
	}

	// ── Drivers ──────────────────────────────────────────────────────────────
	if err := r.db.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE created_at >= $1 AND created_at < $2),
		  count(*) FILTER (WHERE approval_status = 'PENDING'),
		  count(*) FILTER (WHERE approval_status = 'APPROVED'),
		  count(*) FILTER (WHERE is_online),
		  count(*) FILTER (WHERE approval_status = 'APPROVED' AND (
		      license_expiry_date       BETWEEN CURRENT_DATE AND CURRENT_DATE + 30
		   OR insurance_expiry_date     BETWEEN CURRENT_DATE AND CURRENT_DATE + 30
		   OR authorization_expiry_date BETWEEN CURRENT_DATE AND CURRENT_DATE + 30))
		FROM driver_profiles
	`, start, end).Scan(
		&s.NewDrivers, &s.PendingApplications, &s.ApprovedDrivers, &s.OnlineNow, &s.ExpiringDocuments,
	); err != nil {
		return nil, err
	}

	// ── Packages ─────────────────────────────────────────────────────────────
	if err := r.db.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(price_paid_rwf), 0)::bigint
		FROM package_purchases
		WHERE paid_at >= $1 AND paid_at < $2
	`, start, end).Scan(&s.PackagesSold, &s.PackageRevenue); err != nil {
		return nil, err
	}

	// Payment outcomes grouped as they actually appear, so a new status value
	// shows up in the digest instead of being silently excluded by a filter.
	rows, err := r.db.Query(ctx, `
		SELECT status, count(*) FROM payments
		WHERE created_at >= $1 AND created_at < $2 GROUP BY status
	`, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		s.PaymentsByState[st] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// ── Support load + platform health ───────────────────────────────────────
	if err := r.db.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM support_tickets  WHERE status IN ('OPEN','PENDING')),
		  (SELECT count(*) FROM safety_incidents WHERE status IN ('OPEN','ACKNOWLEDGED','ESCALATED')),
		  (SELECT count(*) FROM driver_documents WHERE uploaded_at >= $1 AND uploaded_at < $2),
		  pg_size_pretty(pg_database_size(current_database()))
	`, start, end).Scan(&s.OpenTickets, &s.OpenIncidents, &s.DocumentsUploaded, &s.DBSize); err != nil {
		return nil, err
	}

	return s, nil
}
