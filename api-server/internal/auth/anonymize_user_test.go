package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTx implements pgx.Tx, capturing every statement executed on it — used
// to assert on the exact SQL AnonymizeUser runs inside its transaction.
// Mirrors internal/admin/service_test.go's mockTx.
type mockTx struct {
	execSQLs  []string
	execErr   error
	committed bool
	rolledBk  bool
}

func (t *mockTx) Begin(ctx context.Context) (pgx.Tx, error) { return nil, nil }
func (t *mockTx) Commit(ctx context.Context) error          { t.committed = true; return nil }
func (t *mockTx) Rollback(ctx context.Context) error        { t.rolledBk = true; return nil }
func (t *mockTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.execSQLs = append(t.execSQLs, sql)
	return pgconn.CommandTag{}, t.execErr
}
func (t *mockTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (t *mockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return errRow(pgx.ErrNoRows)
}
func (t *mockTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *mockTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults { return nil }
func (t *mockTx) LargeObjects() pgx.LargeObjects                             { return pgx.LargeObjects{} }
func (t *mockTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *mockTx) Conn() *pgx.Conn { return nil }

// ── AnonymizeUser: gender is scrubbed on account deletion ─────────────────
//
// Register now writes gender as a standard registration attribute (this
// branch's own change). Before this fix, DeleteAccount's anonymization swept
// every other PII column (phone, name, email, device/fcm ids, national ID)
// but left gender behind — a deleted account permanently retained a declared
// personal attribute, still joined to the surviving users.id row. No
// pre-existing test covered AnonymizeUser at all (checked: no repo hits for
// AnonymizeUser/DeleteAccount under *_test.go before this file), so this is
// a new regression test, not an amendment to one.
func TestAnonymizeUser_ScrubsGender(t *testing.T) {
	tx := &mockTx{}
	repo := &Repository{db: &mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	}}

	err := repo.AnonymizeUser(context.Background(), "user-to-delete")
	require.NoError(t, err)
	require.True(t, tx.committed, "AnonymizeUser must commit its transaction on success")
	// AnonymizeUser defers tx.Rollback(ctx) unconditionally (the standard pgx
	// pattern — a no-op against an already-committed real transaction), so
	// rolledBk being true here is expected and not asserted against.

	var usersUpdateSQL string
	for _, sql := range tx.execSQLs {
		if strings.Contains(sql, "UPDATE users") && strings.Contains(sql, "phone_number = 'del_'") {
			usersUpdateSQL = sql
		}
	}
	require.NotEmpty(t, usersUpdateSQL, "expected the users PII-scrub UPDATE to run")
	assert.Contains(t, usersUpdateSQL, "gender = NULL",
		"gender must be cleared alongside the rest of PII on account deletion")
}
