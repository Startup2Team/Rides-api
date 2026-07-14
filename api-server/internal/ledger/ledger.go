// Package ledger is the double-entry general ledger: an append-only journal of
// balanced entries that the finance reports (GL, Trial Balance, Balance Sheet)
// read from. Postings originate at money-affecting events (today: package
// sales). See docs/backend/LEDGER_DESIGN.md.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Chart-of-accounts codes (seeded by migration 061).
const (
	AcctCashMoMo          = "1000" // Cash & Bank — MoMo (asset)
	AcctCashManual        = "1010" // Cash & Bank — Manual cash/bank (asset)
	AcctDriverWallet      = "2000" // Driver Wallet Balances (liability)
	AcctDeferredRevenue   = "2100" // Deferred Revenue — Unused Ride Credits (liability)
	AcctRetainedEarnings  = "3000" // Retained Earnings (equity)
	AcctPackageRevenue    = "4000" // Package Sales Revenue (revenue)
	AcctCommissionRevenue = "4100" // Commission Revenue (revenue)
	AcctPromoExpense      = "5000" // Promotional Credits (expense)
	AcctProcessingFees    = "5100" // Payment Processing Fees (expense)
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, so a caller can post
// inside its own transaction (atomic with the originating write) or standalone.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Line is one leg of a journal entry: exactly one of Debit/Credit is non-zero.
type Line struct {
	Account string
	Debit   int64
	Credit  int64
	Memo    string
}

// Entry is a balanced set of lines recording a single economic event.
type Entry struct {
	Date           time.Time
	Description    string
	SourceType     string
	SourceID       *string
	IdempotencyKey string
	CreatedBy      string
	Lines          []Line
}

// Service posts entries to the journal.
type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// DB exposes the underlying pool for callers that want to post standalone.
func (s *Service) DB() *pgxpool.Pool { return s.db }

// Post validates that the entry is balanced and writes it via q (a tx or the
// pool). It is idempotent on IdempotencyKey: a duplicate post is a silent no-op,
// which makes it safe under webhook + reconcile + admin races.
func (s *Service) Post(ctx context.Context, q Querier, e Entry) error {
	if len(e.Lines) < 2 {
		return errors.New("ledger: an entry needs at least two lines")
	}
	if e.IdempotencyKey == "" {
		return errors.New("ledger: idempotency key is required")
	}
	var totalDebit, totalCredit int64
	for _, l := range e.Lines {
		if l.Account == "" {
			return errors.New("ledger: line has empty account code")
		}
		if (l.Debit == 0) == (l.Credit == 0) {
			return fmt.Errorf("ledger: line for %s must have exactly one of debit/credit set", l.Account)
		}
		if l.Debit < 0 || l.Credit < 0 {
			return fmt.Errorf("ledger: line for %s has a negative amount", l.Account)
		}
		totalDebit += l.Debit
		totalCredit += l.Credit
	}
	if totalDebit != totalCredit {
		return fmt.Errorf("ledger: unbalanced entry (debit %d != credit %d)", totalDebit, totalCredit)
	}

	createdBy := e.CreatedBy
	if createdBy == "" {
		createdBy = "system"
	}

	var entryID string
	err := q.QueryRow(ctx, `
		INSERT INTO journal_entries (entry_date, description, source_type, source_id, idempotency_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`,
		e.Date, e.Description, e.SourceType, e.SourceID, e.IdempotencyKey, createdBy,
	).Scan(&entryID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already posted for this economic event — idempotent no-op.
		return nil
	}
	if err != nil {
		return fmt.Errorf("ledger: insert entry: %w", err)
	}

	for _, l := range e.Lines {
		if _, err := q.Exec(ctx, `
			INSERT INTO journal_lines (entry_id, account_code, debit_rwf, credit_rwf, memo)
			VALUES ($1, $2, $3, $4, $5)`,
			entryID, l.Account, l.Debit, l.Credit, l.Memo,
		); err != nil {
			return fmt.Errorf("ledger: insert line (%s): %w", l.Account, err)
		}
	}
	return nil
}
