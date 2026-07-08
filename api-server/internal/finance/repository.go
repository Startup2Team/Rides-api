package finance

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

type DBWalletTransaction struct {
	ID          string
	Type        string
	AmountRWF   int64
	Description string
	ExternalRef string
	Status      string
	CreatedAt   time.Time
}

type DBPayment struct {
	ID              string
	RideID          string
	AmountRWF       int64
	PlatformFeeRWF  int64
	DriverAmountRWF int64
	PaymentMethod   string
	Status          string
	PaidAt          *time.Time
	CreatedAt       time.Time
}

func (r *Repository) GetWalletTransactions(ctx context.Context, start, end *time.Time) ([]DBWalletTransaction, error) {
	query := `
		SELECT id, type, amount_rwf, COALESCE(description, ''), COALESCE(external_ref, ''), status, created_at
		FROM wallet_transactions
		WHERE status = 'COMPLETED'
	`
	var args []interface{}
	n := 1
	if start != nil {
		query += ` AND created_at >= $` + itoa(n)
		args = append(args, *start)
		n++
	}
	if end != nil {
		query += ` AND created_at <= $` + itoa(n)
		args = append(args, *end)
		n++
	}
	query += ` ORDER BY created_at ASC`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txns []DBWalletTransaction
	for rows.Next() {
		var t DBWalletTransaction
		if err := rows.Scan(&t.ID, &t.Type, &t.AmountRWF, &t.Description, &t.ExternalRef, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		txns = append(txns, t)
	}
	return txns, nil
}

func (r *Repository) GetPayments(ctx context.Context, start, end *time.Time) ([]DBPayment, error) {
	query := `
		SELECT id, ride_id, amount_rwf, platform_fee_rwf, driver_amount_rwf, payment_method, status, paid_at, created_at
		FROM payments
		WHERE status = 'COMPLETED' OR status = 'SUCCESS' OR status = 'PAID'
	`
	var args []interface{}
	n := 1
	if start != nil {
		query += ` AND created_at >= $` + itoa(n)
		args = append(args, *start)
		n++
	}
	if end != nil {
		query += ` AND created_at <= $` + itoa(n)
		args = append(args, *end)
		n++
	}
	query += ` ORDER BY created_at ASC`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payments []DBPayment
	for rows.Next() {
		var p DBPayment
		if err := rows.Scan(&p.ID, &p.RideID, &p.AmountRWF, &p.PlatformFeeRWF, &p.DriverAmountRWF, &p.PaymentMethod, &p.Status, &p.PaidAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		payments = append(payments, p)
	}
	return payments, nil
}

func (r *Repository) GetStaffActivity(ctx context.Context) ([]StaffActivity, error) {
	query := `
		SELECT a.id, a.name, a.email, r.name as role,
		       (SELECT COUNT(*) FROM admin_audit_log WHERE admin_id = a.id) as action_count,
		       (SELECT MAX(occurred_at) FROM admin_audit_log WHERE admin_id = a.id) as last_active
		FROM admin_accounts a
		JOIN admin_roles r ON a.role_id = r.id
		ORDER BY action_count DESC
	`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []StaffActivity
	for rows.Next() {
		var sa StaffActivity
		if err := rows.Scan(&sa.AdminID, &sa.Name, &sa.Email, &sa.Role, &sa.ActionCount, &sa.LastActive); err != nil {
			return nil, err
		}
		activities = append(activities, sa)
	}
	return activities, nil
}

func (r *Repository) GetStaffCount(ctx context.Context) (int, int, error) {
	var total, active int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_accounts`).Scan(&total)
	if err != nil {
		return 0, 0, err
	}
	err = r.db.QueryRow(ctx, `SELECT COUNT(DISTINCT admin_id) FROM admin_audit_log WHERE occurred_at >= NOW() - INTERVAL '30 days'`).Scan(&active)
	if err != nil {
		return total, 0, nil
	}
	return total, active, nil
}

func (r *Repository) GetTotalActions(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit_log`).Scan(&count)
	return count, err
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
