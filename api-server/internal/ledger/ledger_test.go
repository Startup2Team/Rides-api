package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// fakeRow / fakeQuerier let us exercise Post without a database.
type fakeRow struct {
	err error
	id  string
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*string); ok {
			*p = r.id
		}
	}
	return nil
}

type fakeQuerier struct {
	rowErr    error
	id        string
	queryRows int
	execs     int
}

func (q *fakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.queryRows++
	return fakeRow{err: q.rowErr, id: q.id}
}

func (q *fakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.execs++
	return pgconn.CommandTag{}, nil
}

func balancedEntry() Entry {
	return Entry{
		Date:           time.Unix(1_700_000_000, 0),
		Description:    "Package sale ref-1",
		SourceType:     "package_purchase",
		IdempotencyKey: "purchase_paid:abc",
		Lines: []Line{
			{Account: AcctCashMoMo, Debit: 2000},
			{Account: AcctPackageRevenue, Credit: 2000},
		},
	}
}

func TestPost_BalancedWritesHeaderAndLines(t *testing.T) {
	q := &fakeQuerier{id: "entry-1"}
	s := &Service{}
	err := s.Post(context.Background(), q, balancedEntry())
	assert.NoError(t, err)
	assert.Equal(t, 1, q.queryRows, "one header insert")
	assert.Equal(t, 2, q.execs, "one insert per line")
}

func TestPost_RejectsUnbalanced(t *testing.T) {
	q := &fakeQuerier{id: "entry-1"}
	e := balancedEntry()
	e.Lines[1].Credit = 1999 // debit 2000 != credit 1999
	err := (&Service{}).Post(context.Background(), q, e)
	assert.ErrorContains(t, err, "unbalanced")
	assert.Equal(t, 0, q.queryRows, "must validate before touching the DB")
}

func TestPost_RejectsLineWithBothSides(t *testing.T) {
	q := &fakeQuerier{id: "entry-1"}
	e := balancedEntry()
	e.Lines[0] = Line{Account: AcctCashMoMo, Debit: 2000, Credit: 2000}
	err := (&Service{}).Post(context.Background(), q, e)
	assert.Error(t, err)
	assert.Equal(t, 0, q.queryRows)
}

func TestPost_RejectsMissingIdempotencyKey(t *testing.T) {
	e := balancedEntry()
	e.IdempotencyKey = ""
	err := (&Service{}).Post(context.Background(), &fakeQuerier{}, e)
	assert.Error(t, err)
}

func TestPost_IdempotentNoOpOnConflict(t *testing.T) {
	// ON CONFLICT DO NOTHING → RETURNING yields no row → pgx.ErrNoRows.
	q := &fakeQuerier{rowErr: pgx.ErrNoRows}
	err := (&Service{}).Post(context.Background(), q, balancedEntry())
	assert.NoError(t, err, "duplicate post is a silent no-op")
	assert.Equal(t, 0, q.execs, "no line inserts when the entry already exists")
}

// ── RevenueBetween ────────────────────────────────────────────────────────────

// revenueRow fills the three int64 destinations RevenueBetween scans into.
type revenueRow struct {
	err                          error
	total, packageSales, commiss int64
}

func (r revenueRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	vals := []int64{r.total, r.packageSales, r.commiss}
	for i, v := range vals {
		if i >= len(dest) {
			break
		}
		if p, ok := dest[i].(*int64); ok {
			*p = v
		}
	}
	return nil
}

type revenueQuerier struct {
	row     revenueRow
	lastSQL string
	args    []any
}

func (q *revenueQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.lastSQL, q.args = sql, args
	return q.row
}

func (q *revenueQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func TestRevenueBetween_MapsColumnsToFields(t *testing.T) {
	q := &revenueQuerier{row: revenueRow{total: 19500, packageSales: 19500, commiss: 0}}
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(24 * time.Hour)

	rev, err := RevenueBetween(context.Background(), q, from, to)
	assert.NoError(t, err)
	assert.Equal(t, int64(19500), rev.Total)
	assert.Equal(t, int64(19500), rev.PackageSales)
	assert.Equal(t, int64(0), rev.Commission)

	// The window must be passed through, not silently widened to "everything".
	assert.Equal(t, from, q.args[0])
	assert.Equal(t, to, q.args[1])
}

func TestRevenueBetween_PropagatesError(t *testing.T) {
	// The bug this replaces discarded scan errors with `_ =`, so a broken query
	// was indistinguishable from genuinely zero revenue.
	q := &revenueQuerier{row: revenueRow{err: pgx.ErrNoRows}}
	_, err := RevenueBetween(context.Background(), q, time.Unix(0, 0), time.Unix(1, 0))
	assert.Error(t, err, "a failed revenue query must not read as zero revenue")
}

func TestRevenueBetween_CountsOnlyRevenueAccountsNetOfDebits(t *testing.T) {
	q := &revenueQuerier{}
	_, _ = RevenueBetween(context.Background(), q, time.Unix(0, 0), time.Unix(1, 0))

	// Guards the two properties that make this correct: it is scoped to
	// REVENUE-type accounts (not ride fares, which live on rides.agreed_fare),
	// and it nets debits off credits so a refund reduces revenue.
	assert.Contains(t, q.lastSQL, "la.type = 'REVENUE'")
	assert.Contains(t, q.lastSQL, "credit_rwf - jl.debit_rwf")
	assert.NotContains(t, q.lastSQL, "agreed_fare")
}
