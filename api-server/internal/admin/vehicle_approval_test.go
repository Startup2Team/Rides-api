package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Per-vehicle approval (migration 089): ApproveVehicle/RejectVehicle are the
// per-vehicle counterpart of ApproveDriver/RejectDriver — they set ONE
// vehicle's approval_status, independent of every other vehicle that driver
// owns and of driver_profiles.approval_status itself.
//
// These are pure Service-layer unit tests against mockDB (no live Postgres
// needed) — RowsAffected is the ONLY signal these methods have for both "not
// found" and "not this driver's vehicle" (IDOR), since the UPDATE's WHERE
// clause matches on id AND driver_id together, so both are proven the same
// way scanRow/mockDB proves every other admin driver-lifecycle transition in
// this package (see drivers_resubmit_test.go).

func TestApproveVehicle_Success(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			gotSQL = sql
			gotArgs = args
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})

	err := svc.ApproveVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid")
	require.NoError(t, err)
	assert.Contains(t, gotSQL, "approval_status = 'APPROVED'")
	// WHERE id = $1 AND driver_id = $2 is the IDOR guard: both the vehicle id
	// AND the profile id from the URL must be bound, or a vehicle belonging to
	// a different driver could be approved via this driver's URL.
	require.Len(t, gotArgs, 2)
	assert.Equal(t, "vehicle-123", gotArgs[0])
	assert.Equal(t, "profile-xyz", gotArgs[1])
}

func TestApproveVehicle_WrongDriverOrMissing_NotFound(t *testing.T) {
	// RowsAffected == 0 covers BOTH "no such vehicle" and "vehicle exists but
	// belongs to a different driver" (the IDOR case) — the WHERE clause
	// matches neither, and the caller cannot tell them apart, which is
	// deliberate: confirming a foreign vehicle id exists is its own leak.
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
	})

	err := svc.ApproveVehicle(context.Background(), "profile-xyz", "someone-elses-vehicle", "admin-uuid")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

func TestApproveVehicle_DBError(t *testing.T) {
	dbErr := errors.New("connection refused")
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, dbErr
		},
	})

	err := svc.ApproveVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid")
	assert.ErrorIs(t, err, dbErr)
}

func TestRejectVehicle_Success_CapturesReason(t *testing.T) {
	var gotArgs []any
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			gotArgs = args
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "insurance photo illegible")
	require.NoError(t, err)
	require.Len(t, gotArgs, 3)
	assert.Equal(t, "vehicle-123", gotArgs[0])
	assert.Equal(t, "profile-xyz", gotArgs[1])
	assert.Equal(t, "insurance photo illegible", gotArgs[2])
}

func TestRejectVehicle_EmptyReason_PassesNil(t *testing.T) {
	// Mirrors RejectDriver's own handler, which never requires a reason
	// either (RejectDriver's own body.Reason is optional at the handler
	// layer) — an empty string must not be stored as a literal empty-string
	// rejection_reason, it must be NULL.
	var gotArgs []any
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			gotArgs = args
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "")
	require.NoError(t, err)
	require.Len(t, gotArgs, 3)
	assert.Nil(t, gotArgs[2])
}

func TestRejectVehicle_WrongDriverOrMissing_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "someone-elses-vehicle", "admin-uuid", "reason")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}
