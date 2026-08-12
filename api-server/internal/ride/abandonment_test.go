package ride

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/workspace/ride-platform/pkg/geo"
)

// ── Abandonment watchdog eligibility ──────────────────────────────────────

func TestAbandonment_EligibilityDecision(t *testing.T) {
	const (
		active  = 3 * time.Minute
		onboard = 10 * time.Minute
	)
	now := time.Now()

	cases := []struct {
		name            string
		status          Status
		stalledFor      time.Duration
		locationFresh   bool
		socketConnected bool
		want            bool
	}{
		{
			name:   "en-route, silent past threshold — cancel",
			status: StatusDriverEnRoute, stalledFor: 4 * time.Minute,
			locationFresh: false, socketConnected: false,
			want: true,
		},
		{
			name:   "confirmed, silent past threshold — cancel",
			status: StatusConfirmed, stalledFor: 3 * time.Minute,
			locationFresh: false, socketConnected: false,
			want: true,
		},
		{
			name:   "arrived, silent but ride stalled under threshold — keep",
			status: StatusDriverArrived, stalledFor: 2 * time.Minute,
			locationFresh: false, socketConnected: false,
			want: false,
		},
		{
			name:   "en-route, stalled but location key still fresh — keep",
			status: StatusDriverEnRoute, stalledFor: 30 * time.Minute,
			locationFresh: true, socketConnected: false,
			want: false,
		},
		{
			name:   "en-route, stalled, location expired but socket alive — keep",
			status: StatusDriverEnRoute, stalledFor: 30 * time.Minute,
			locationFresh: false, socketConnected: true,
			want: false,
		},
		{
			name:   "in-progress uses the longer onboard threshold — keep at 5min",
			status: StatusInProgress, stalledFor: 5 * time.Minute,
			locationFresh: false, socketConnected: false,
			want: false,
		},
		{
			name:   "in-progress, silent past onboard threshold — cancel",
			status: StatusInProgress, stalledFor: 11 * time.Minute,
			locationFresh: false, socketConnected: false,
			want: true,
		},
		{
			name:   "statuses outside the watchdog are never touched",
			status: StatusNegotiating, stalledFor: time.Hour,
			locationFresh: false, socketConnected: false,
			want: false,
		},
		{
			name:   "terminal status is never touched",
			status: StatusCompleted, stalledFor: time.Hour,
			locationFresh: false, socketConnected: false,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eligibleForAbandonCancel(
				tc.status, now.Add(-tc.stalledFor), now,
				active, onboard,
				tc.locationFresh, tc.socketConnected,
			)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAbandonment_ThresholdPerStatus(t *testing.T) {
	active, onboard := 3*time.Minute, 10*time.Minute

	for _, st := range []Status{StatusConfirmed, StatusDriverEnRoute, StatusDriverArrived} {
		d, ok := abandonThreshold(st, active, onboard)
		assert.True(t, ok, "%s must be covered", st)
		assert.Equal(t, active, d, "%s uses the pre-boarding threshold", st)
	}

	d, ok := abandonThreshold(StatusInProgress, active, onboard)
	assert.True(t, ok)
	assert.Equal(t, onboard, d, "IN_PROGRESS uses the longer onboard threshold")

	for _, st := range []Status{StatusSearching, StatusMatched, StatusNegotiating, StatusCompleted, StatusCancelled} {
		_, ok := abandonThreshold(st, active, onboard)
		assert.False(t, ok, "%s must not be covered by the watchdog", st)
	}
}

// ── Finalizer near-destination decision ───────────────────────────────────

func TestFinalizer_NearDestinationDecision(t *testing.T) {
	// Kigali city centre as the destination.
	dest := geo.Point{Lat: -1.9441, Lng: 30.0619}

	// ~100m north of the destination (1 deg lat ≈ 111km).
	near := geo.Point{Lat: dest.Lat + 100.0/111000.0, Lng: dest.Lng}
	// ~2km north — nowhere near a plausible drop-off.
	far := geo.Point{Lat: dest.Lat + 2000.0/111000.0, Lng: dest.Lng}

	assert.True(t, finalizeNearDestination(near, true, dest, finalizeNearDestinationM),
		"a driver last seen ~100m from the destination completed the trip")
	assert.False(t, finalizeNearDestination(far, true, dest, finalizeNearDestinationM),
		"a driver last seen 2km away did not finish this trip")
	assert.False(t, finalizeNearDestination(geo.Point{}, false, dest, finalizeNearDestinationM),
		"no last-known location = no evidence the trip happened = no payout")
	assert.True(t, finalizeNearDestination(dest, true, dest, finalizeNearDestinationM),
		"exactly at the destination completes")
}
