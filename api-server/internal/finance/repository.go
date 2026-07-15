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

// accountBalance is one account's net movement (Σ debit − Σ credit) up to a
// point in time, tagged with its accounting type for balance-sheet classification.
type accountBalance struct {
	Name    string
	Type    string
	Balance int64 // sum(debit_rwf - credit_rwf)
}

// GetJournalEntries returns the general ledger — one row per posted journal line
// — over an optional date window, ordered chronologically.
func (r *Repository) GetJournalEntries(ctx context.Context, start, end *time.Time) ([]LedgerEntry, error) {
	query := `
		SELECT e.entry_date, e.id::text, a.name, e.description, l.debit_rwf, l.credit_rwf,
		       COALESCE(e.source_id::text, '')
		FROM journal_lines l
		JOIN journal_entries e ON e.id = l.entry_id
		JOIN ledger_accounts a ON a.code = l.account_code
		WHERE 1=1`
	var args []interface{}
	n := 1
	if start != nil {
		query += ` AND e.entry_date >= $` + itoa(n)
		args = append(args, *start)
		n++
	}
	if end != nil {
		query += ` AND e.entry_date <= $` + itoa(n)
		args = append(args, *end)
		n++
	}
	query += ` ORDER BY e.entry_date ASC, e.id, l.id`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.Date, &e.TransactionID, &e.Account, &e.Description, &e.Debit, &e.Credit, &e.Reference); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetTrialBalanceRows aggregates posted lines into per-account debit/credit
// totals, plus the grand totals (which reconcile iff the journal is intact).
func (r *Repository) GetTrialBalanceRows(ctx context.Context, start, end *time.Time) ([]TrialBalanceRow, int64, int64, error) {
	query := `
		SELECT a.name, COALESCE(SUM(l.debit_rwf),0), COALESCE(SUM(l.credit_rwf),0)
		FROM journal_lines l
		JOIN journal_entries e ON e.id = l.entry_id
		JOIN ledger_accounts a ON a.code = l.account_code
		WHERE 1=1`
	var args []interface{}
	n := 1
	if start != nil {
		query += ` AND e.entry_date >= $` + itoa(n)
		args = append(args, *start)
		n++
	}
	if end != nil {
		query += ` AND e.entry_date <= $` + itoa(n)
		args = append(args, *end)
		n++
	}
	query += ` GROUP BY a.name ORDER BY a.name`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	var out []TrialBalanceRow
	var totalDebit, totalCredit int64
	for rows.Next() {
		var row TrialBalanceRow
		if err := rows.Scan(&row.Account, &row.DebitTotal, &row.CreditTotal); err != nil {
			return nil, 0, 0, err
		}
		totalDebit += row.DebitTotal
		totalCredit += row.CreditTotal
		out = append(out, row)
	}
	return out, totalDebit, totalCredit, rows.Err()
}

// GetAccountBalances returns each account's net balance as of a date (nil = all
// time), excluding accounts with no net movement, tagged with accounting type.
func (r *Repository) GetAccountBalances(ctx context.Context, asOf *time.Time) ([]accountBalance, error) {
	query := `
		SELECT a.name, a.type,
		       COALESCE(SUM(
		           CASE WHEN ($1::timestamptz IS NULL OR e.entry_date <= $1::timestamptz)
		                THEN l.debit_rwf - l.credit_rwf ELSE 0 END
		       ), 0) AS bal
		FROM ledger_accounts a
		LEFT JOIN journal_lines l   ON l.account_code = a.code
		LEFT JOIN journal_entries e ON e.id = l.entry_id
		GROUP BY a.name, a.type
		HAVING COALESCE(SUM(
		           CASE WHEN ($1::timestamptz IS NULL OR e.entry_date <= $1::timestamptz)
		                THEN l.debit_rwf - l.credit_rwf ELSE 0 END
		       ), 0) <> 0
		ORDER BY a.name`

	rows, err := r.db.Query(ctx, query, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []accountBalance
	for rows.Next() {
		var b accountBalance
		if err := rows.Scan(&b.Name, &b.Type, &b.Balance); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repository) GetStaffActivity(ctx context.Context) ([]StaffActivity, error) {
	query := `
		SELECT a.id, a.name, a.email, r.name as role,
		       COUNT(l.id) as action_count,
		       MAX(l.occurred_at) as last_active
		FROM admin_accounts a
		JOIN admin_roles r ON a.role_id = r.id
		LEFT JOIN admin_audit_log l ON l.admin_id = a.id
		GROUP BY a.id, a.name, a.email, r.name
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
