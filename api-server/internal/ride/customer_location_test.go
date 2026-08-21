package ride

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── Customer live-location share gate ─────────────────────────────────────
//
// customerLocationShareEligible is the pure decision behind
// Service.UpdateCustomerLocation's "reject a non-active ride" rule (item 2 of
// the customer-location contract): whole-trip sharing, gated to
// CustomerLocationShareStatuses, and never fanned out without an assigned
// driver even if that ever drifts from the status set.

func TestCustomerLocationShareEligible(t *testing.T) {
	driverID := "driver-profile-1"

	cases := []struct {
		name     string
		status   Status
		driverID *string
		want     bool
	}{
		{"searching — no driver assigned yet", StatusSearching, nil, false},
		{"matched — no driver assigned yet", StatusMatched, nil, false},
		{"negotiating — no driver assigned yet", StatusNegotiating, nil, false},
		{"confirmed with driver — eligible", StatusConfirmed, &driverID, true},
		{"driver en route with driver — eligible", StatusDriverEnRoute, &driverID, true},
		{"driver arrived with driver — eligible", StatusDriverArrived, &driverID, true},
		{"in progress with driver — eligible", StatusInProgress, &driverID, true},
		{"completed ride — rejected even with a driver on record", StatusCompleted, &driverID, false},
		{"cancelled ride — rejected even with a driver on record", StatusCancelled, &driverID, false},
		{"active status but no driver on record — defensive reject", StatusConfirmed, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := customerLocationShareEligible(tc.status, tc.driverID)
			assert.Equal(t, tc.want, got)
		})
	}
}
