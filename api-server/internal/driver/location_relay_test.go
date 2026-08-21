package driver

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// fakeWSNotifier records every NotifyCustomer call so tests can assert on the
// fan-out without a real WebSocket hub.
type fakeWSNotifier struct {
	rideID  string
	msgType string
	payload map[string]interface{}
	calls   int
}

func (f *fakeWSNotifier) NotifyCustomer(rideID, msgType string, payload map[string]interface{}) {
	f.rideID = rideID
	f.msgType = msgType
	f.payload = payload
	f.calls++
}

func newTestService(t *testing.T) (*Service, *goredis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return &Service{redis: rdb, log: zerolog.Nop()}, rdb
}

// ── relayLocationToCustomer (moved from the WS read pump into the REST path) ──

func TestRelayLocationToCustomer_NoActiveRide_NoOp(t *testing.T) {
	s, _ := newTestService(t)
	notifier := &fakeWSNotifier{}
	s.SetWSNotifier(notifier)

	// No driver:<id>:active_ride key set — nothing to relay to.
	s.relayLocationToCustomer(context.Background(), "driver-1", -1.9441, 30.0619)

	assert.Zero(t, notifier.calls, "must not fan out when the driver has no active ride")
}

func TestRelayLocationToCustomer_NoNotifierWired_NoOp(t *testing.T) {
	s, rdb := newTestService(t)
	// wsNotifier intentionally left nil (main.go always wires it, but a nil
	// notifier must not panic — e.g. a future test harness that skips wiring).
	require.NoError(t, rdb.Set(context.Background(), rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())

	assert.NotPanics(t, func() {
		s.relayLocationToCustomer(context.Background(), "driver-1", -1.9441, 30.0619)
	})
}

func TestRelayLocationToCustomer_FirstUpdate_NoSmoothingYet(t *testing.T) {
	s, rdb := newTestService(t)
	notifier := &fakeWSNotifier{}
	s.SetWSNotifier(notifier)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())

	s.relayLocationToCustomer(ctx, "driver-1", -1.9441, 30.0619)

	require.Equal(t, 1, notifier.calls)
	assert.Equal(t, "ride-1", notifier.rideID)
	assert.Equal(t, "driver_location", notifier.msgType)
	assert.Equal(t, -1.9441, notifier.payload["lat"])
	assert.Equal(t, 30.0619, notifier.payload["lng"])

	// Cached for reconnecting customers.
	cached, err := rdb.Get(ctx, rkeys.K.RideDriverLocation("ride-1")).Result()
	require.NoError(t, err)
	assert.Contains(t, cached, "-1.9441")
}

func TestRelayLocationToCustomer_SecondUpdate_AppliesEMASmoothing(t *testing.T) {
	s, rdb := newTestService(t)
	notifier := &fakeWSNotifier{}
	s.SetWSNotifier(notifier)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverActiveRide("driver-1"), "ride-1", 0).Err())

	// Seed a previous smoothed position, as if a prior update had landed.
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverSmoothedLocation("driver-1"), `{"lat":0.0,"lng":0.0}`, 0).Err())
	// prevLoc.Lat/Lng == 0 is treated as "no prior smoothing yet" (see
	// relayLocationToCustomer) — seed a genuine non-zero previous point instead.
	require.NoError(t, rdb.Set(ctx, rkeys.K.DriverSmoothedLocation("driver-1"), `{"lat":-2.0,"lng":30.0}`, 0).Err())

	s.relayLocationToCustomer(ctx, "driver-1", -1.0, 31.0)

	require.Equal(t, 1, notifier.calls)
	// α=0.4: smoothed = 0.4*new + 0.6*prev
	const emaAlpha = 0.4
	wantLat := emaAlpha*-1.0 + (1-emaAlpha)*-2.0
	wantLng := emaAlpha*31.0 + (1-emaAlpha)*30.0
	assert.InDelta(t, wantLat, notifier.payload["lat"], 1e-9)
	assert.InDelta(t, wantLng, notifier.payload["lng"], 1e-9)
}
