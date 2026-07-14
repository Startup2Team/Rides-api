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
