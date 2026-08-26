package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── RequestDriverMoreInfo: notification (KYC resubmission loop fix) ───────
//
// RequestDriverMoreInfo used to send NO notification at all — a driver moved
// to NEEDS_MORE_INFO had no way to learn why short of noticing their status
// changed on the app's next poll. These tests prove it now mirrors
// RejectDriver's notify pattern.

// fakeNotifierCall records one SendToAllDevices invocation.
type fakeNotifierCall struct {
	userID, title, body, nType string
	data                       map[string]string
}

// fakeNotifier is a test double for admin.Notifier.
type fakeNotifier struct {
	calls []fakeNotifierCall
}

func (f *fakeNotifier) SendToAllDevices(_ context.Context, userID, title, body, nType string, data map[string]string) {
	f.calls = append(f.calls, fakeNotifierCall{userID, title, body, nType, data})
}

func TestRequestDriverMoreInfo_SendsNotification(t *testing.T) {
	notifier := &fakeNotifier{}
	svc := newTestService(&mockDB{
		// RequestDriverMoreInfo first looks up the driver's user_id (to notify them).
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-user-uuid")
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})
	svc.SetNotifier(notifier)

	docs := []DriverMoreInfoDocument{
		{DocumentType: "INSURANCE", Comment: "photo is blurry"},
		{DocumentType: "DRIVER_LICENSE_FRONT", Comment: ""}, // blank comment must be skipped, not appended as noise
	}
	err := svc.RequestDriverMoreInfo(context.Background(), "profile-xyz", "admin-uuid", "documents unclear", docs)
	require.NoError(t, err)

	require.Len(t, notifier.calls, 1)
	call := notifier.calls[0]
	assert.Equal(t, "driver-user-uuid", call.userID)
	assert.Equal(t, "driver_more_info_requested", call.data["type"])
	assert.Equal(t, "documents unclear", call.data["reason"])
	assert.Contains(t, call.body, "documents unclear")
	assert.Contains(t, call.body, "INSURANCE: photo is blurry.")
	assert.NotContains(t, call.body, "DRIVER_LICENSE_FRONT", "a blank per-document comment must not be appended")
}

func TestRequestDriverMoreInfo_NoNotifierWired_StillSucceeds(t *testing.T) {
	// No SetNotifier call — mirrors production before a Notifier is wired in
	// main.go, and every existing admin test in this package that doesn't
	// wire one. Must not panic on a nil s.notifier.
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-user-uuid")
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	})
	err := svc.RequestDriverMoreInfo(context.Background(), "profile-xyz", "admin-uuid", "reason", nil)
	require.NoError(t, err)
}

func TestRequestDriverMoreInfo_ReasonRequired(t *testing.T) {
	svc := newTestService(&mockDB{})
	err := svc.RequestDriverMoreInfo(context.Background(), "profile-xyz", "admin-uuid", "   ", nil)
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "REASON_REQUIRED", appErr.Code)
}

func TestRequestDriverMoreInfo_NotFound(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	})
	err := svc.RequestDriverMoreInfo(context.Background(), "profile-xyz", "admin-uuid", "reason", nil)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

func TestRequestDriverMoreInfo_InvalidState(t *testing.T) {
	// RowsAffected == 0 means the WHERE guard (approval_status IN
	// ('PENDING_REVIEW','NEEDS_MORE_INFO')) matched nothing — e.g. the driver
	// is APPROVED or REJECTED, not currently in review.
	notifier := &fakeNotifier{}
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-user-uuid")
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		},
	})
	svc.SetNotifier(notifier)

	err := svc.RequestDriverMoreInfo(context.Background(), "profile-xyz", "admin-uuid", "reason", nil)
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "INVALID_STATE", appErr.Code)
	assert.Empty(t, notifier.calls, "must not notify when the transition didn't actually happen")
}

// ── ListDrivers: review-queue ordering (KYC resubmission loop fix) ────────
//
// reopenForReview (internal/driver.Service) bumps updated_at on every
// resubmission but leaves created_at untouched. Without ordering the review
// queue by updated_at, a resubmitted driver stayed buried under their
// original application date instead of floating back to the top where a
// reviewer will see them.

func TestListDrivers_OrdersByUpdatedAtForReviewQueue(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		sort        string
		wantOrderBy string
	}{
		{"pending_review default", "PENDING_REVIEW", "", "ORDER BY dp.updated_at DESC"},
		{"needs_more_info default", "NEEDS_MORE_INFO", "", "ORDER BY dp.updated_at DESC"},
		{"approved default unchanged", "APPROVED", "", "ORDER BY dp.created_at DESC"},
		{"no status filter unchanged", "", "", "ORDER BY dp.created_at DESC"},
		{"explicit updated_at sort overrides", "APPROVED", "updated_at", "ORDER BY dp.updated_at DESC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQL string
			svc := newTestService(&mockDB{
				queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
					return scanRow(0) // COUNT(*)
				},
				queryFn: func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
					gotSQL = sql
					return &emptyRows{}, nil
				},
			})
			_, _, err := svc.ListDrivers(context.Background(), tc.status, "", "", tc.sort, 20, 0)
			require.NoError(t, err)
			assert.Contains(t, gotSQL, tc.wantOrderBy)
			assert.Contains(t, gotSQL, "dp.updated_at", "updated_at must be selected so the caller can see it")
		})
	}
}
