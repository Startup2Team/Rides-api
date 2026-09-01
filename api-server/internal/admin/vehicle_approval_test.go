package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
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
	// RejectVehicle's core UPDATE now uses QueryRow ... RETURNING is_active
	// (not Exec) — it needs to know, atomically with the write, whether the
	// rejected row was the driver's ACTIVE vehicle so it can decide whether to
	// run the online-driver eviction (RejectVehicle's own doc comment). A
	// non-active vehicle (is_active=false, as here) must not trigger any
	// further profile lookup — the mockDB's default queryRowFn (errRow(
	// pgx.ErrNoRows)) covers "no further QueryRow calls happen" for us: if
	// RejectVehicle wrongly proceeded to the eviction path, the unstubbed
	// second QueryRow call would surface as a NOT_FOUND error here.
	var gotSQL string
	var gotArgs []any
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
			gotSQL = sql
			gotArgs = args
			return scanRow(false) // is_active
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "insurance photo illegible")
	require.NoError(t, err)
	assert.Contains(t, gotSQL, "approval_status = 'REJECTED'")
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
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			gotArgs = args
			return scanRow(false) // is_active
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "")
	require.NoError(t, err)
	require.Len(t, gotArgs, 3)
	assert.Nil(t, gotArgs[2])
}

func TestRejectVehicle_WrongDriverOrMissing_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "someone-elses-vehicle", "admin-uuid", "reason")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

// TestRejectVehicle_ActiveVehicleOfOfflineDriver_NoEviction proves the
// eviction path is skipped (no forced is_online write, no Redis touch) when
// the driver is not currently online — mirrors SuspendDriver/reopenForReview,
// which likewise only evict an ALREADY-online driver. The mock's UPDATE
// driver_profiles SET is_online = FALSE branch is asserted absent via gotSQL
// staying on the driver_profiles SELECT only.
func TestRejectVehicle_ActiveVehicleOfOfflineDriver_NoEviction(t *testing.T) {
	var execCalls int
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, sql string, _ ...any) pgx.Row {
			if strings.Contains(sql, "UPDATE driver_vehicles") {
				return scanRow(true) // is_active
			}
			// SELECT user_id, transport_type, is_online FROM driver_profiles
			return scanRow("user-1", "MOTO_BIKE", false)
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalls++
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "stale insurance")
	require.NoError(t, err)
	assert.Zero(t, execCalls, "an offline driver's active-vehicle rejection must not force is_online=false")
}

// TestRejectVehicle_ActiveVehicleOfOnlineDriver_ForcesOffline is the P1
// regression this fix closes: rejecting an ONLINE driver's ACTIVE vehicle
// must force is_online=false in Postgres (Redis/FCM eviction is covered
// end-to-end by test/dbit's
// TestAdminRejectVehicle_ActiveVehicleOfOnlineDriver_ForcesOfflineAndEvictsFromGeoIndex,
// which needs a real Postgres+Redis to observe the geo index).
func TestRejectVehicle_ActiveVehicleOfOnlineDriver_ForcesOffline(t *testing.T) {
	var sawForceOffline bool
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, sql string, _ ...any) pgx.Row {
			if strings.Contains(sql, "UPDATE driver_vehicles") {
				return scanRow(true) // is_active
			}
			return scanRow("user-1", "MOTO_BIKE", true) // is_online
		},
		execFn: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "SET is_online = FALSE") {
				sawForceOffline = true
				require.Len(t, args, 1)
				assert.Equal(t, "profile-xyz", args[0])
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})

	err := svc.RejectVehicle(context.Background(), "profile-xyz", "vehicle-123", "admin-uuid", "stale insurance")
	require.NoError(t, err)
	assert.True(t, sawForceOffline, "rejecting an online driver's active vehicle must force is_online=false")
}
