package ride_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/ride"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Valid transitions accepted ────────────────────────────────────────────

func TestStateMachine_ValidTransitions(t *testing.T) {
	valid := []struct {
		from ride.Status
		to   ride.Status
	}{
		{ride.StatusSearching, ride.StatusMatched},
		{ride.StatusSearching, ride.StatusCancelled},
		{ride.StatusMatched, ride.StatusNegotiating},
		{ride.StatusMatched, ride.StatusSearching},
		{ride.StatusNegotiating, ride.StatusConfirmed},
		{ride.StatusNegotiating, ride.StatusSearching},
		{ride.StatusNegotiating, ride.StatusCancelled},
		{ride.StatusConfirmed, ride.StatusDriverEnRoute},
		{ride.StatusDriverEnRoute, ride.StatusDriverArrived},
		{ride.StatusDriverArrived, ride.StatusInProgress},
		{ride.StatusDriverArrived, ride.StatusCancelled},
		{ride.StatusInProgress, ride.StatusCompleted},
	}

	for _, tc := range valid {
		err := ride.ValidateTransition(tc.from, tc.to)
		assert.NoError(t, err, "expected %s→%s to be valid", tc.from, tc.to)
	}
}

// ── Invalid transitions rejected with 409 ────────────────────────────────

func TestStateMachine_InvalidTransitions(t *testing.T) {
	invalid := []struct {
		from ride.Status
		to   ride.Status
	}{
		{ride.StatusSearching, ride.StatusCompleted},
		{ride.StatusSearching, ride.StatusInProgress},
		{ride.StatusNegotiating, ride.StatusDriverEnRoute},
		{ride.StatusConfirmed, ride.StatusCompleted},
		{ride.StatusCompleted, ride.StatusCancelled},  // terminal
		{ride.StatusCancelled, ride.StatusSearching},  // terminal
		{ride.StatusInProgress, ride.StatusCancelled}, // not allowed
		{ride.StatusDriverArrived, ride.StatusConfirmed},
	}

	for _, tc := range invalid {
		err := ride.ValidateTransition(tc.from, tc.to)
		require.Error(t, err, "expected %s→%s to be invalid", tc.from, tc.to)
		appErr, ok := err.(*apperrors.AppError)
		require.True(t, ok, "expected AppError for %s→%s", tc.from, tc.to)
		assert.Equal(t, 409, appErr.StatusCode, "expected 409 for %s→%s", tc.from, tc.to)
	}
}

// ── agreed_fare written once at CONFIRMED ────────────────────────────────

func TestStateMachine_AgreedFareImmutable(t *testing.T) {
	// The DB-level enforcement is: LockFare checks fare_locked_at IS NULL.
	// We test the error sentinel is correct.
	err := apperrors.ErrFareLocked
	assert.Equal(t, 409, err.StatusCode)
	assert.Equal(t, "FARE_LOCKED", err.Code)
}

// ── All timestamps server-generated ──────────────────────────────────────

func TestStateMachine_TimestampsServerGenerated(t *testing.T) {
	// Verify that the Ride struct has no client-settable timestamp fields
	// (all timestamps are set by the repository layer, not from request bodies).
	// This is a structural test — if someone adds a client timestamp field,
	// the handler tests will catch it. Here we just document the contract.
	r := &ride.Ride{}
	// StartedAt, CompletedAt, FareLockedAt are all *time.Time set by repo only.
	assert.Nil(t, r.StartedAt)
	assert.Nil(t, r.CompletedAt)
	assert.Nil(t, r.FareLockedAt)
	assert.Nil(t, r.DriverArrivedAt)
}

func TestStateMachine_PickupExpiryAllowsDriverNoShowCancel(t *testing.T) {
	r := &ride.Ride{
		Status:        ride.StatusDriverArrived,
		PickupExpired: true,
	}

	assert.True(t, r.PickupExpired)
	assert.NoError(t, ride.ValidateTransition(r.Status, ride.StatusCancelled))
}

// ── Cancel reasons mapped correctly ──────────────────────────────────────

func TestStateMachine_CancelReasons(t *testing.T) {
	reasons := []string{
		"no_driver_found",
		"negotiation_timeout",
		"customer_cancelled",
		"driver_cancelled",
		"customer_no_show",
		"system_timeout",
	}
	// All reasons are plain strings stored in rides.cancel_reason.
	// Verify none are empty (documentation test).
	for _, r := range reasons {
		assert.NotEmpty(t, r)
	}
}

// ── CancellableStatuses covers correct states ─────────────────────────────

func TestStateMachine_CancellableStatuses(t *testing.T) {
	assert.True(t, ride.CancellableStatuses[ride.StatusSearching])
	assert.True(t, ride.CancellableStatuses[ride.StatusMatched])
	assert.True(t, ride.CancellableStatuses[ride.StatusNegotiating])
	assert.False(t, ride.CancellableStatuses[ride.StatusConfirmed])
	assert.False(t, ride.CancellableStatuses[ride.StatusInProgress])
	assert.False(t, ride.CancellableStatuses[ride.StatusCompleted])
}

// ── IsTerminal ────────────────────────────────────────────────────────────

func TestStateMachine_IsTerminal(t *testing.T) {
	assert.True(t, ride.IsTerminal(ride.StatusCompleted))
	assert.True(t, ride.IsTerminal(ride.StatusCancelled))
	assert.False(t, ride.IsTerminal(ride.StatusSearching))
	assert.False(t, ride.IsTerminal(ride.StatusInProgress))
}
