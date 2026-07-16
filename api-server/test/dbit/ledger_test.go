//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/ledger"
)

func TestLedger_PostBalancedEntryWritesRows(t *testing.T) {
	ctx := context.Background()
	svc := ledger.NewService(pool)
	key := uniqueKey("sale")

	entry := ledger.Entry{
		Date:           time.Now(),
		Description:    "integration test sale",
		SourceType:     "test",
		IdempotencyKey: key,
		CreatedBy:      "dbit",
		Lines: []ledger.Line{
			{Account: "1000", Debit: 500, Memo: "cash in"},
			{Account: "4000", Credit: 500, Memo: "revenue"},
		},
	}
	require.NoError(t, svc.Post(ctx, pool, entry))

	var entryID string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM journal_entries WHERE idempotency_key = $1`, key).Scan(&entryID))

	var nLines int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM journal_lines WHERE entry_id = $1`, entryID).Scan(&nLines))
	require.Equal(t, 2, nLines)

	// Double-entry invariant enforced by the schema/service: debits == credits.
	var debit, credit int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(debit_rwf),0), COALESCE(SUM(credit_rwf),0)
		   FROM journal_lines WHERE entry_id = $1`, entryID).Scan(&debit, &credit))
	require.Equal(t, debit, credit)
	require.EqualValues(t, 500, debit)
}

func TestLedger_PostIsIdempotentOnKey(t *testing.T) {
	ctx := context.Background()
	svc := ledger.NewService(pool)
	key := uniqueKey("dup")
	entry := ledger.Entry{
		Date:           time.Now(),
		Description:    "idempotency probe",
		SourceType:     "test",
		IdempotencyKey: key,
		CreatedBy:      "dbit",
		Lines: []ledger.Line{
			{Account: "1000", Debit: 100},
			{Account: "4000", Credit: 100},
		},
	}
	// Posting the same economic event twice (webhook + reconcile race) must
	// leave exactly one journal entry.
	require.NoError(t, svc.Post(ctx, pool, entry))
	require.NoError(t, svc.Post(ctx, pool, entry))

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM journal_entries WHERE idempotency_key = $1`, key).Scan(&n))
	require.Equal(t, 1, n, "duplicate post on the same key must be a no-op")
}

func TestLedger_UnbalancedEntryRejected(t *testing.T) {
	err := ledger.NewService(pool).Post(context.Background(), pool, ledger.Entry{
		Date:           time.Now(),
		IdempotencyKey: uniqueKey("bad"),
		CreatedBy:      "dbit",
		Lines: []ledger.Line{
			{Account: "1000", Debit: 100},
			{Account: "4000", Credit: 50}, // debits != credits
		},
	})
	require.Error(t, err, "an unbalanced entry must be rejected before any row is written")
}
