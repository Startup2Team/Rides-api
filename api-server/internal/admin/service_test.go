package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── pgx mock primitives ───────────────────────────────────────────────────

// closureRow implements pgx.Row using a caller-supplied Scan function.
type closureRow struct {
	scanFn func(dest ...any) error
}

func (r *closureRow) Scan(dest ...any) error { return r.scanFn(dest...) }

func errRow(err error) pgx.Row {
	return &closureRow{scanFn: func(...any) error { return err }}
}

func scanRow(values ...any) pgx.Row {
	return &closureRow{scanFn: func(dest ...any) error {
		for i, d := range dest {
			if i >= len(values) {
				break
			}
			if values[i] == nil {
				continue
			}
			switch p := d.(type) {
			case *string:
				if v, ok := values[i].(string); ok {
					*p = v
				}
			case **string:
				if v, ok := values[i].(string); ok {
					*p = &v
				}
			case *int:
				if v, ok := values[i].(int); ok {
					*p = v
				}
			case **int:
				if v, ok := values[i].(int); ok {
					*p = &v
				}
			case *int64:
				switch v := values[i].(type) {
				case int64:
					*p = v
				case int:
					*p = int64(v)
				}
			case *float64:
				switch v := values[i].(type) {
				case float64:
					*p = v
				case int:
					*p = float64(v)
				}
			case *bool:
				if v, ok := values[i].(bool); ok {
					*p = v
				}
			}
		}
		return nil
	}}
}

// emptyRows returns a pgx.Rows that has no rows.
type emptyRows struct{}

func (r *emptyRows) Close()                                       {}
func (r *emptyRows) Err() error                                   { return nil }
func (r *emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *emptyRows) Next() bool                                   { return false }
func (r *emptyRows) Scan(...any) error                            { return nil }
func (r *emptyRows) Values() ([]any, error)                       { return nil, nil }
func (r *emptyRows) RawValues() [][]byte                          { return nil }
func (r *emptyRows) Conn() *pgx.Conn                              { return nil }

// errRows returns a pgx.Rows that immediately errors on Query.
type errRows struct{ err error }

func (r *errRows) Close()                                       {}
func (r *errRows) Err() error                                   { return r.err }
func (r *errRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *errRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *errRows) Next() bool                                   { return false }
func (r *errRows) Scan(...any) error                            { return r.err }
func (r *errRows) Values() ([]any, error)                       { return nil, nil }
func (r *errRows) RawValues() [][]byte                          { return nil }
func (r *errRows) Conn() *pgx.Conn                              { return nil }

// mockTx implements pgx.Tx for testing SuspendDriver / ReinstateDriver.
type mockTx struct {
	execErr   error
	committed bool
}

func (t *mockTx) Begin(ctx context.Context) (pgx.Tx, error) { return nil, nil }
func (t *mockTx) Commit(ctx context.Context) error          { t.committed = true; return nil }
func (t *mockTx) Rollback(ctx context.Context) error        { return nil }
func (t *mockTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, t.execErr
}
func (t *mockTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return &emptyRows{}, nil
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

// mockDB implements DBTX with per-call function fields for fine-grained control.
type mockDB struct {
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	execFn     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	queryFn    func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	beginFn    func(ctx context.Context) (pgx.Tx, error)
}

func (m *mockDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if m.queryRowFn != nil {
		return m.queryRowFn(ctx, sql, args...)
	}
	return errRow(pgx.ErrNoRows)
}
func (m *mockDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if m.execFn != nil {
		return m.execFn(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}
func (m *mockDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, sql, args...)
	}
	return &emptyRows{}, nil
}
func (m *mockDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if m.beginFn != nil {
		return m.beginFn(ctx)
	}
	return &mockTx{}, nil
}

// newTestService creates a Service wired to a mockDB (no real DB, no real logger).
func newTestService(db DBTX) *Service {
	return &Service{db: db, log: zerolog.Nop()}
}

// ── Pure function tests ───────────────────────────────────────────────────

func TestBuildWhere_Empty(t *testing.T) {
	assert.Equal(t, "", buildWhere(nil))
	assert.Equal(t, "", buildWhere([]string{}))
}

func TestBuildWhere_SingleClause(t *testing.T) {
	assert.Equal(t, " WHERE approval_status = 'APPROVED'", buildWhere([]string{"approval_status = 'APPROVED'"}))
}

func TestBuildWhere_MultipleClauses(t *testing.T) {
	got := buildWhere([]string{"status = 'ACTIVE'", "city = 'Kigali'"})
	assert.Equal(t, " WHERE status = 'ACTIVE' AND city = 'Kigali'", got)
}

func TestPeriodToInterval_AllPeriods(t *testing.T) {
	cases := []struct {
		period   string
		expected string
	}{
		{"week", "INTERVAL '7 days'"},
		{"month", "INTERVAL '30 days'"},
		{"quarter", "INTERVAL '90 days'"},
		{"year", "INTERVAL '365 days'"},
		{"today", "INTERVAL '1 day'"},
		{"", "INTERVAL '1 day'"},
		{"unknown", "INTERVAL '1 day'"},
	}
	for _, tc := range cases {
		t.Run(tc.period, func(t *testing.T) {
			assert.Equal(t, tc.expected, periodToInterval(tc.period))
		})
	}
}

// ── ApproveDriver ─────────────────────────────────────────────────────────

func TestApproveDriver_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

func TestApproveDriver_SelfApproval(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("admin-uuid", "MOTO_BIKE") // driverUserID == adminUserID
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	assert.True(t, errors.Is(err, apperrors.ErrSelfApproval))
}

func TestApproveDriver_Success(t *testing.T) {
	execCount := 0
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-uuid", "MOTO_BIKE")
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCount++
			return pgconn.CommandTag{}, nil
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.NoError(t, err)
	assert.Equal(t, 2, execCount)
}

func TestApproveDriver_DBError(t *testing.T) {
	dbErr := errors.New("connection refused")
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(dbErr)
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	assert.ErrorIs(t, err, dbErr)
}

// ── RejectDriver ──────────────────────────────────────────────────────────

func TestRejectDriver_Success(t *testing.T) {
	called := false
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			called = true
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})
	err := svc.RejectDriver(context.Background(), "profile-xyz", "admin-uuid", "incomplete docs")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRejectDriver_DBError(t *testing.T) {
	dbErr := errors.New("exec failed")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})
	err := svc.RejectDriver(context.Background(), "profile-xyz", "admin-uuid", "reason")
	assert.ErrorIs(t, err, dbErr)
}

// ── SuspendDriver (uses transaction) ─────────────────────────────────────

func TestSuspendDriver_Success(t *testing.T) {
	tx := &mockTx{}
	svc := newTestService(&mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("MOTO_BIKE")
		},
		beginFn: func(_ context.Context) (pgx.Tx, error) { return tx, nil },
	})
	err := svc.SuspendDriver(context.Background(), "profile-xyz", "admin-uuid", "fraud", 48)
	require.NoError(t, err)
	assert.True(t, tx.committed)
}

func TestSuspendDriver_TxExecError(t *testing.T) {
	dbErr := errors.New("tx exec failed")
	tx := &mockTx{execErr: dbErr}
	svc := newTestService(&mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("MOTO_BIKE")
		},
		beginFn: func(_ context.Context) (pgx.Tx, error) { return tx, nil },
	})
	err := svc.SuspendDriver(context.Background(), "profile-xyz", "admin-uuid", "fraud", 24)
	assert.ErrorIs(t, err, dbErr)
}

func TestSuspendDriver_BeginError(t *testing.T) {
	dbErr := errors.New("begin failed")
	svc := newTestService(&mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("MOTO_BIKE")
		},
		beginFn: func(_ context.Context) (pgx.Tx, error) { return nil, dbErr },
	})
	err := svc.SuspendDriver(context.Background(), "profile-xyz", "admin-uuid", "reason", 24)
	assert.ErrorIs(t, err, dbErr)
}

// ── ReinstateDriver (uses transaction) ───────────────────────────────────

func TestReinstateDriver_Success(t *testing.T) {
	tx := &mockTx{}
	svc := newTestService(&mockDB{
		beginFn: func(_ context.Context) (pgx.Tx, error) { return tx, nil },
	})
	err := svc.ReinstateDriver(context.Background(), "profile-xyz")
	require.NoError(t, err)
	assert.True(t, tx.committed)
}

func TestReinstateDriver_TxExecError(t *testing.T) {
	dbErr := errors.New("tx exec failed")
	tx := &mockTx{execErr: dbErr}
	svc := newTestService(&mockDB{
		beginFn: func(_ context.Context) (pgx.Tx, error) { return tx, nil },
	})
	err := svc.ReinstateDriver(context.Background(), "profile-xyz")
	assert.ErrorIs(t, err, dbErr)
}

// ── SuspendUser / ReinstateUser ───────────────────────────────────────────

func TestSuspendUser_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.SuspendUser(context.Background(), "user-uuid", 72))
}

func TestSuspendUser_DBError(t *testing.T) {
	dbErr := errors.New("exec failed")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})
	assert.ErrorIs(t, svc.SuspendUser(context.Background(), "user-uuid", 24), dbErr)
}

func TestSuspendUser_RevokesSessions(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := goredis.NewClient(&goredis.Options{
		Addr: mr.Addr(),
	})
	defer rdb.Close()

	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	svc.SetRedis(rdb)

	ctx := context.Background()
	_ = rdb.Set(ctx, "session:user-uuid:jti1", "valid", 0).Err()
	_ = rdb.Set(ctx, "session:user-uuid:jti2", "valid", 0).Err()
	_ = rdb.Set(ctx, "session:other-user:jti3", "valid", 0).Err()

	err = svc.SuspendUser(ctx, "user-uuid", 24)
	assert.NoError(t, err)

	assert.False(t, mr.Exists("session:user-uuid:jti1"))
	assert.False(t, mr.Exists("session:user-uuid:jti2"))
	assert.True(t, mr.Exists("session:other-user:jti3"))
}

func TestReinstateUser_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.ReinstateUser(context.Background(), "user-uuid"))
}

// ── UpdateCustomer / BanCustomer ──────────────────────────────────────────

func TestUpdateCustomer_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.UpdateCustomer(context.Background(), "user-uuid", "Active", "ok"))
}

func TestBanCustomer_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.BanCustomer(context.Background(), "user-uuid", "fraud"))
}

func TestBanCustomer_DBError(t *testing.T) {
	dbErr := errors.New("exec failed")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})
	assert.ErrorIs(t, svc.BanCustomer(context.Background(), "user-uuid", "reason"), dbErr)
}

// ── UpdateDriver / DeleteDriver ───────────────────────────────────────────

func TestUpdateDriver_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.UpdateDriver(context.Background(), "profile-uuid", map[string]interface{}{"vehicle_plate": "RAD 001 A"}))
}

func TestUpdateDriver_RejectedField(t *testing.T) {
	svc := newTestService(&mockDB{})
	err := svc.UpdateDriver(context.Background(), "profile-uuid", map[string]interface{}{"invalid_col": "some value"})
	assert.Error(t, err)
	var appErr *apperrors.AppError
	assert.True(t, errors.As(err, &appErr))
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_FIELD", appErr.Code)
}

func TestUpdateDriver_SQLInjectionAttempt(t *testing.T) {
	svc := newTestService(&mockDB{})
	err := svc.UpdateDriver(context.Background(), "profile-uuid", map[string]interface{}{"approval_status = APPROVED; DROP TABLE users;--": 1})
	assert.Error(t, err)
	var appErr *apperrors.AppError
	assert.True(t, errors.As(err, &appErr))
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_FIELD", appErr.Code)
}

func TestUpdateDriver_EmptyFields(t *testing.T) {
	svc := newTestService(&mockDB{})
	// empty fields still executes (service doesn't validate, handler does)
	err := svc.UpdateDriver(context.Background(), "profile-uuid", map[string]interface{}{})
	assert.NoError(t, err)
}

func TestDeleteDriver_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	})
	assert.NoError(t, svc.DeleteDriver(context.Background(), "profile-uuid"))
}

func TestDeleteDriver_DBError(t *testing.T) {
	dbErr := errors.New("exec failed")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})
	assert.ErrorIs(t, svc.DeleteDriver(context.Background(), "profile-uuid"), dbErr)
}

// ── InterveneRide ─────────────────────────────────────────────────────────

func TestInterveneRide_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			// A live ride was updated (RowsAffected=1) — passes the state guard.
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})
	assert.NoError(t, svc.InterveneRide(context.Background(), "ride-uuid", "cancel", "admin action"))
}

func TestInterveneRide_DBError(t *testing.T) {
	dbErr := errors.New("exec failed")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})
	assert.ErrorIs(t, svc.InterveneRide(context.Background(), "ride-uuid", "cancel", "reason"), dbErr)
}

// ── ListDrivers / ListCustomers / ListRides — empty DB returns empty slice ─

func TestListDrivers_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0) // COUNT returns 0
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	drivers, total, err := svc.ListDrivers(context.Background(), "", "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, drivers)
}

func TestListDrivers_QueryError(t *testing.T) {
	dbErr := errors.New("query failed")
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return nil, dbErr
		},
	})
	_, _, err := svc.ListDrivers(context.Background(), "", "", "", "", 20, 0)
	assert.ErrorIs(t, err, dbErr)
}

func TestListCustomers_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	customers, total, err := svc.ListCustomers(context.Background(), "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, customers)
}

func TestListRides_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	rides, total, err := svc.ListRides(context.Background(), "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, rides)
}

func TestListNegotiations_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	negs, total, err := svc.ListNegotiations(context.Background(), "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, negs)
}

func TestListLiveRides_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	rides, total, err := svc.ListLiveRides(context.Background(), "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, rides)
}

func TestListTransactions_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	txns, total, err := svc.ListTransactions(context.Background(), "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, txns)
}

// ── GetCustomer — NotFound ────────────────────────────────────────────────

func TestGetCustomer_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	_, err := svc.GetCustomer(context.Background(), "unknown-uuid")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

// ── GetRide — NotFound ────────────────────────────────────────────────────

func TestGetRide_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	_, err := svc.GetRide(context.Background(), "unknown-uuid")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

// ── GetDriver — NotFound ──────────────────────────────────────────────────

func TestGetDriver_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	_, err := svc.GetDriver(context.Background(), "unknown-uuid")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

// ── GPSAnomalies / DeviceCollisions — QueryError ──────────────────────────

func TestGPSAnomalies_EmptyResult(t *testing.T) {
	svc := newTestService(&mockDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	anomalies, err := svc.GPSAnomalies(context.Background(), 100)
	require.NoError(t, err)
	assert.Empty(t, anomalies)
}

func TestGPSAnomalies_QueryError(t *testing.T) {
	dbErr := errors.New("query failed")
	svc := newTestService(&mockDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return nil, dbErr
		},
	})
	_, err := svc.GPSAnomalies(context.Background(), 100)
	assert.ErrorIs(t, err, dbErr)
}

func TestDeviceCollisions_EmptyResult(t *testing.T) {
	svc := newTestService(&mockDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	collisions, err := svc.DeviceCollisions(context.Background())
	require.NoError(t, err)
	assert.Empty(t, collisions)
}

// ── DriverOverview — all QueryRow calls return 0 ──────────────────────────

func TestDriverOverview_AllZero(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(0)
		},
	})
	data, err := svc.DriverOverview(context.Background(), "")
	require.NoError(t, err)
	assert.NotNil(t, data)
}

// ── RevenueKPIs — returns map with period ────────────────────────────────

func TestRevenueKPIs_ReturnsData(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(float64(0))
		},
	})
	data, err := svc.RevenueKPIs(context.Background(), "today")
	require.NoError(t, err)
	assert.NotNil(t, data)
}

// ── SetPackagesService ────────────────────────────────────────────────────

func TestSetPackagesService(t *testing.T) {
	svc := newTestService(&mockDB{})
	assert.Nil(t, svc.packages)
	svc.SetPackagesService(nil)
	assert.Nil(t, svc.packages)
}

// ── funcRows — multi-row mock for Query-based tests ───────────────────────

// funcRows implements pgx.Rows using per-row scan closures.
type funcRows struct {
	scanFns []func(dest ...any) error
	idx     int
}

func (r *funcRows) Close()                                       {}
func (r *funcRows) Err() error                                   { return nil }
func (r *funcRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *funcRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *funcRows) Next() bool                                   { r.idx++; return r.idx <= len(r.scanFns) }
func (r *funcRows) Scan(dest ...any) error                       { return r.scanFns[r.idx-1](dest...) }
func (r *funcRows) Values() ([]any, error)                       { return nil, nil }
func (r *funcRows) RawValues() [][]byte                          { return nil }
func (r *funcRows) Conn() *pgx.Conn                              { return nil }

// ── NewService constructor ────────────────────────────────────────────────

func TestNewService_Constructor(t *testing.T) {
	db := &mockDB{}
	svc := NewService(db, zerolog.Nop())
	require.NotNil(t, svc)
	assert.Equal(t, db, svc.db)
}

// ── GetLiveRide / GetNegotiation — delegate to GetRide ───────────────────

func TestGetLiveRide_DelegatesToGetRide_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	_, err := svc.GetLiveRide(context.Background(), "unknown")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

func TestGetNegotiation_DelegatesToGetRide_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	_, err := svc.GetNegotiation(context.Background(), "unknown")
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

// ── InterveneRide — all branches ──────────────────────────────────────────

func TestInterveneRide_ForceComplete(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})
	assert.NoError(t, svc.InterveneRide(context.Background(), "ride-id", "force-complete", "admin"))
}

func TestInterveneRide_UnknownAction(t *testing.T) {
	svc := newTestService(&mockDB{})
	err := svc.InterveneRide(context.Background(), "ride-id", "delete", "reason")
	require.Error(t, err)
}

// ── Revenue ───────────────────────────────────────────────────────────────

func TestRevenue_EmptyDB(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(float64(0), 0) // gross=0, trips=0
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	data, err := svc.Revenue(context.Background(), "week")
	require.NoError(t, err)
	assert.Equal(t, "week", data["period"])
	assert.Equal(t, float64(0), data["gross"])
}

func TestRevenue_WithGross(t *testing.T) {
	call := 0
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			call++
			if call == 1 {
				return scanRow(float64(100000), 50) // gross, trips
			}
			return scanRow(float64(80000), 40) // prev gross, prev trips
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	data, err := svc.Revenue(context.Background(), "month")
	require.NoError(t, err)
	assert.Equal(t, float64(100000), data["gross"])
}

// ── DisbursePayouts ───────────────────────────────────────────────────────

func TestDisbursePayouts_EmptyList(t *testing.T) {
	svc := newTestService(&mockDB{})
	_, _, err := svc.DisbursePayouts(context.Background(), []string{})
	assert.True(t, errors.Is(err, apperrors.ErrBadRequest))
}

func TestDisbursePayouts_Success(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(float64(10000))
		},
	})
	count, total, err := svc.DisbursePayouts(context.Background(), []string{"tx-1", "tx-2"})
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, 10000*0.85, total)
}

// ── GetRide — success path (row scanning) ─────────────────────────────────

func TestGetRide_Success(t *testing.T) {
	now := time.Now()
	queryCount := 0
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			queryCount++
			return &closureRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = "ride-id"
				*dest[1].(*string) = "COMPLETED"
				*dest[2].(*string) = "MOTO_BIKE"
				*dest[3].(*string) = "cust-id"
				*dest[4].(*string) = "+250780000000"
				// dest[5..9] are **string (nullable: custName, driverID, driverPhone, driverName, plate) — leave nil
				*dest[10].(*string) = "Kigali CBD"
				*dest[11].(*string) = "Kimironko"
				// dest[12..14] are **float64 (nullable: agreedFare, initialFare, distKm) — leave nil
				*dest[15].(*time.Time) = now
				// dest[16] is **time.Time (nullable: completedAt) — leave nil
				return nil
			}}
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	data, err := svc.GetRide(context.Background(), "ride-id")
	require.NoError(t, err)
	assert.Equal(t, "ride-id", data["id"])
	assert.Equal(t, "COMPLETED", data["status"])
}

// ── ListDrivers — with one row to exercise scan loop ─────────────────────

func TestListDrivers_WithOneRow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(1)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					*dest[0].(*string) = "dp-id"
					*dest[1].(*string) = "user-id"
					*dest[2].(*string) = "+250780000000"
					// dest[3] = **string (fullName nullable)
					*dest[4].(*string) = "MOTO_BIKE"
					*dest[5].(*string) = "RAD 001 A"
					*dest[6].(*string) = "APPROVED"
					*dest[7].(*int) = 1
					*dest[8].(*int) = 10
					*dest[9].(*float64) = 0.95
					*dest[10].(*bool) = true
					// dest[11] = **string (city nullable)
					*dest[12].(*time.Time) = now
					return nil
				},
			}}, nil
		},
	})
	drivers, total, err := svc.ListDrivers(context.Background(), "APPROVED", "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, drivers, 1)
	assert.Equal(t, "dp-id", drivers[0]["id"])
}

// ── ListCustomers — with one row ──────────────────────────────────────────

func TestListCustomers_WithOneRow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(1)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// Scan order: id, phone, email(**), fullName(**), roleState,
					//   isSuspended, suspensionUntil(**), createdAt, lastSeenAt(**),
					//   rating, totalRides, totalSpend
					*dest[0].(*string) = "user-id"
					*dest[1].(*string) = "+250780000000"
					// dest[2] = **string (email, nullable) - leave nil
					// dest[3] = **string (fullName, nullable) - leave nil
					*dest[4].(*string) = "CUSTOMER_ONLY"
					*dest[5].(*bool) = false
					// dest[6] = **time.Time (suspensionUntil, nullable) - leave nil
					*dest[7].(*time.Time) = now
					// dest[8] = **time.Time (lastSeenAt, nullable) - leave nil
					*dest[9].(*float64) = 5.0
					*dest[10].(*int) = 5
					*dest[11].(*float64) = 25000.0
					return nil
				},
			}}, nil
		},
	})
	customers, total, err := svc.ListCustomers(context.Background(), "", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, customers, 1)
}

// ── ListRides — with one row ──────────────────────────────────────────────

// ── GetCustomer — success path ────────────────────────────────────────────

func TestGetCustomer_Success(t *testing.T) {
	now := time.Now()
	call := 0
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			call++
			if call == 1 {
				// Main user row: id, phone, email(**), fullName(**), roleState,
				// isSuspended, suspensionUntil(**), createdAt, lastSeenAt(**), rating
				return &closureRow{scanFn: func(dest ...any) error {
					*dest[0].(*string) = "user-id"
					*dest[1].(*string) = "+250780000000"
					// dest[2] = **string (email, nullable) - leave nil
					// dest[3] = **string (fullName, nullable) - leave nil
					*dest[4].(*string) = "CUSTOMER_ONLY"
					*dest[5].(*bool) = false
					// dest[6] = **time.Time (suspensionUntil, nullable) - leave nil
					*dest[7].(*time.Time) = now
					// dest[8] = **time.Time (lastSeenAt, nullable) - leave nil
					*dest[9].(*float64) = 5.0
					return nil
				}}
			}
			// Second call: totalRides, totalSpend
			return &closureRow{scanFn: func(dest ...any) error {
				*dest[0].(*int) = 3
				*dest[1].(*float64) = 9000.0
				return nil
			}}
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &emptyRows{}, nil
		},
	})
	data, err := svc.GetCustomer(context.Background(), "user-id")
	require.NoError(t, err)
	assert.Equal(t, "user-id", data["id"])
	assert.Equal(t, 3, data["total_rides"])
}

// ── GPSAnomalies — with one row ───────────────────────────────────────────

func TestGPSAnomalies_WithOneRow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&mockDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// id, driverID, phone, speed, detectedAt
					*dest[0].(*string) = "anomaly-id"
					*dest[1].(*string) = "driver-id"
					*dest[2].(*string) = "+250780000000"
					*dest[3].(*float64) = 250.0
					*dest[4].(*time.Time) = now
					return nil
				},
			}}, nil
		},
	})
	anomalies, err := svc.GPSAnomalies(context.Background(), 100)
	require.NoError(t, err)
	assert.Len(t, anomalies, 1)
	assert.Equal(t, float64(250.0), anomalies[0]["computed_speed_kmh"])
}

// ── DeviceCollisions — with one row ──────────────────────────────────────

func TestDeviceCollisions_WithOneRow(t *testing.T) {
	svc := newTestService(&mockDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// deviceID, userCount, phones([]string)
					*dest[0].(*string) = "device-abc"
					*dest[1].(*int) = 3
					*dest[2].(*[]string) = []string{"+250780000001", "+250780000002"}
					return nil
				},
			}}, nil
		},
	})
	collisions, err := svc.DeviceCollisions(context.Background())
	require.NoError(t, err)
	assert.Len(t, collisions, 1)
	assert.Equal(t, 3, collisions[0]["user_count"])
}

// ── ListNegotiations — with one row ──────────────────────────────────────

func TestListNegotiations_WithOneRow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(1)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// id, rStatus, rType, pickupAddr, destAddr,
					// custPhone, custName(**), driverPhone(**), driverName(**),
					// driverType(**), plate(**), initialFare(**), agreedFare(**),
					// createdAt, roundCount
					*dest[0].(*string) = "ride-id"
					*dest[1].(*string) = "NEGOTIATING"
					*dest[2].(*string) = "CAB_TAXI"
					*dest[3].(*string) = "Kigali CBD"
					*dest[4].(*string) = "Kimironko"
					*dest[5].(*string) = "+250780000000"
					// dest[6..10] = **string (nullable)
					// dest[11..12] = **float64 (nullable)
					*dest[13].(*time.Time) = now
					*dest[14].(*int) = 2
					return nil
				},
			}}, nil
		},
	})
	negs, total, err := svc.ListNegotiations(context.Background(), "InProgress", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, negs, 1)
}

// ── ListTransactions — with one row ──────────────────────────────────────

func TestListTransactions_WithOneRow(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(1)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// id, tType, agreedFare(*f64), pickupAddr, destAddr,
					// custPhone, custName(*str), driverPhone(*str), driverName(*str),
					// plate(*str), completedAt(*time.Time)
					*dest[0].(*string) = "ride-id"
					*dest[1].(*string) = "MOTO_BIKE"
					// dest[2] = **float64 (agreedFare, nullable)
					*dest[3].(*string) = "Kigali CBD"
					*dest[4].(*string) = "Kimironko"
					*dest[5].(*string) = "+250780000000"
					// dest[6..9] = **string (nullable)
					// dest[10] = **time.Time (nullable)
					return nil
				},
			}}, nil
		},
	})
	txns, total, err := svc.ListTransactions(context.Background(), "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, txns, 1)
}

func TestListRides_WithOneRow(t *testing.T) {
	now := time.Now()
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow(1)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &funcRows{scanFns: []func(...any) error{
				func(dest ...any) error {
					// Scan order: id, status, tType, custID, custPhone,
					//   custName(*str), driverID(*str), driverPhone(*str), driverName(*str),
					//   pickupAddr, destAddr, agreedFare(*f64), initialFare(*f64), distKm(*f64),
					//   createdAt, completedAt(*time.Time)
					*dest[0].(*string) = "ride-id"
					*dest[1].(*string) = "COMPLETED"
					*dest[2].(*string) = "MOTO_BIKE"
					*dest[3].(*string) = "cust-id"
					*dest[4].(*string) = "+250780000000"
					// dest[5..8] are **string (nullable) — leave nil
					*dest[9].(*string) = "Kigali CBD"
					*dest[10].(*string) = "Kimironko"
					// dest[11..13] are **float64 (nullable) — leave nil
					*dest[14].(*time.Time) = now
					// dest[15] = **time.Time (nullable) — leave nil
					return nil
				},
			}}, nil
		},
	})
	rides, total, err := svc.ListRides(context.Background(), "COMPLETED", "", "", 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, rides, 1)
}
