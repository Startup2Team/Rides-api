package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// fakeArrivalMarker records every MarkDriverArrivedIfNear call so tests can
// assert on the wiring without a real ride.Service / Postgres.
type fakeArrivalMarker struct {
	rideID          string
	driverProfileID string
	point           geo.Point
	calls           int
	err             error
}

func (f *fakeArrivalMarker) MarkDriverArrivedIfNear(ctx context.Context, rideID, driverProfileID string, point geo.Point) error {
	f.rideID = rideID
	f.driverProfileID = driverProfileID
	f.point = point
	f.calls++
	return f.err
}

// ── maybeAutoMarkArrived (the cheap pre-filter gate before ride.Service) ────

func TestMaybeAutoMarkArrived_NoArrivalMarkerWired_NoOp(t *testing.T) {
	s, rdb := newTestService(t)
	ctx := context.Background()
	// arrivalMarker intentionally left nil — must not panic (mirrors
	// wsNotifier's nil guard for the same reason: not every test harness wires
	// every optional dependency).
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideState("ride-1"), "DRIVER_EN_ROUTE", 0).Err())

	assert.NotPanics(t, func() {
		s.maybeAutoMarkArrived(ctx, "driver-1", geo.Point{Lat: -1.9441, Lng: 30.0619})
	})
}

func TestMaybeAutoMarkArrived_NoActiveRide_NeverCallsMarker(t *testing.T) {
	s, _ := newTestService(t)
	marker := &fakeArrivalMarker{}
	s.SetArrivalMarker(marker)

	// No driver:<id>:active_ride key set.
	s.maybeAutoMarkArrived(context.Background(), "driver-1", geo.Point{Lat: -1.9441, Lng: 30.0619})

	assert.Zero(t, marker.calls, "must not call into ride.Service when the driver has no active ride")
}

func TestMaybeAutoMarkArrived_RideNotEnRoute_NeverCallsMarker(t *testing.T) {
	s, rdb := newTestService(t)
	marker := &fakeArrivalMarker{}
	s.SetArrivalMarker(marker)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideState("ride-1"), "CONFIRMED", 0).Err())

	s.maybeAutoMarkArrived(ctx, "driver-1", geo.Point{Lat: -1.9441, Lng: 30.0619})

	assert.Zero(t, marker.calls, "the cheap Redis gate must skip Postgres entirely outside the EN_ROUTE window")
}

func TestMaybeAutoMarkArrived_NoRideStateCached_NeverCallsMarker(t *testing.T) {
	s, rdb := newTestService(t)
	marker := &fakeArrivalMarker{}
	s.SetArrivalMarker(marker)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())
	// No ride:<id>:state key — must fail closed (no-op), not call through.

	s.maybeAutoMarkArrived(ctx, "driver-1", geo.Point{Lat: -1.9441, Lng: 30.0619})

	assert.Zero(t, marker.calls)
}

func TestMaybeAutoMarkArrived_EnRoute_CallsMarkerWithPoint(t *testing.T) {
	s, rdb := newTestService(t)
	marker := &fakeArrivalMarker{}
	s.SetArrivalMarker(marker)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideState("ride-1"), "DRIVER_EN_ROUTE", 0).Err())

	point := geo.Point{Lat: -1.9441, Lng: 30.0619}
	s.maybeAutoMarkArrived(ctx, "driver-1", point)

	require.Equal(t, 1, marker.calls)
	assert.Equal(t, "ride-1", marker.rideID)
	assert.Equal(t, "driver-1", marker.driverProfileID)
	assert.Equal(t, point, marker.point)
}

func TestMaybeAutoMarkArrived_MarkerError_LoggedNotPanicked(t *testing.T) {
	s, rdb := newTestService(t)
	marker := &fakeArrivalMarker{err: errors.New("boom")}
	s.SetArrivalMarker(marker)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())
	require.NoError(t, rdb.Set(ctx, rkeys.K.RideState("ride-1"), "DRIVER_EN_ROUTE", 0).Err())

	// A failure in the best-effort auto-arrival check must never propagate as
	// a location-update failure — the manual "Arrived" button is the fallback.
	assert.NotPanics(t, func() {
		s.maybeAutoMarkArrived(ctx, "driver-1", geo.Point{Lat: -1.9441, Lng: 30.0619})
	})
	assert.Equal(t, 1, marker.calls)
}
