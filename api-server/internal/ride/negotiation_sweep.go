package ride

import (
	"context"

	"github.com/workspace/ride-platform/internal/tracking"
)

// Negotiation-deadline sweep — the durable backstop for StartNegotiationTimeout.
//
// StartNegotiationTimeout arms an in-process time.AfterFunc, which a
// deploy/restart wipes with no recovery: a ride could then sit NEGOTIATING
// forever (the Redis state key TTLs out after 15 minutes, but the Postgres
// row is never cancelled). This job is that backstop, run on a periodic tick
// from main — belt-and-suspenders alongside the in-memory timer, which still
// fires normally and is the fast path in the overwhelming majority of cases.

// CancelExpiredNegotiations scans rides still NEGOTIATING past their
// persisted deadline (see SetNegotiationDeadline) and cancels them with the
// same consequences as the in-memory timer's fire path (repo state, Redis
// release, WS + FCM to both parties). Returns how many were cancelled.
// Designed to be called on a periodic background tick (see cmd/server/main.go).
func (s *Service) CancelExpiredNegotiations(ctx context.Context) (int, error) {
	candidates, err := s.repo.FindExpiredNegotiations(ctx)
	if err != nil {
		return 0, err
	}

	cancelled := 0
	for _, c := range candidates {
		// Disarm any lingering in-memory timer on this replica first — if the
		// in-memory timer is about to fire on its own, we'd rather it not race
		// this cancel (CancelIfStillNegotiating is safe either way, but this
		// avoids a redundant "ride not found" log from the loser).
		s.CancelNegotiationTimeout(c.ID)

		// Atomic — re-checks status AND deadline at write time, not just at
		// scan time. A ride that progressed (fare agreed) or whose deadline was
		// pushed out (counter-offer) since the scan loses this cleanly; so does
		// a concurrent replica racing the same row.
		didCancel, cerr := s.repo.CancelIfStillNegotiating(ctx, c.ID)
		if cerr != nil {
			s.log.Error().Err(cerr).Str("ride_id", c.ID).Msg("negotiation-sweep: cancel failed")
			continue
		}
		if !didCancel {
			continue
		}

		// NEGOTIATING — no credit was charged yet (charge happens at fare
		// agreement), so no refund is owed here either.
		s.releaseRideRedisState(ctx, c.ID, c.CustomerID, c.DriverID, c.TransportType)
		_ = s.repo.AppendEvent(ctx, c.ID, "ride.cancelled", "SYSTEM", c.ID, map[string]interface{}{
			"reason": "negotiation_timeout",
		})
		s.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", c.ID, &c.ID, map[string]interface{}{
			"ride_id": c.ID, "reason": "negotiation_timeout",
		})
		s.hub.SendToCustomer(c.ID, tracking.Message{
			Type: "ride_cancelled", RideID: c.ID,
			Payload: map[string]interface{}{"reason": "Negotiation timed out. Please request a new ride."},
		})
		// FCM too: the WebSocket only reaches an open app, and a customer whose
		// app is closed would otherwise resume into a ride that no longer exists.
		s.notify.SendToAllDevices(ctx, c.CustomerID, "Ride cancelled",
			"Negotiation timed out. Please request a new ride.", "ride",
			map[string]string{"type": "ride_cancelled", "ride_id": c.ID})
		if c.DriverID != nil {
			s.hub.SendToDriver(*c.DriverID, tracking.Message{
				Type: "ride_cancelled", RideID: c.ID,
				Payload: map[string]interface{}{"reason": "Negotiation timed out."},
			})
			if driverUserID, derr := s.repo.FindDriverUserIDByProfileID(ctx, *c.DriverID); derr == nil {
				s.notify.SendToAllDevices(ctx, driverUserID, "Ride cancelled",
					"Negotiation timed out.", "ride",
					map[string]string{"type": "ride_cancelled", "ride_id": c.ID})
			}
		}

		s.log.Warn().Str("ride_id", c.ID).Msg("negotiation-sweep: cancelled stale NEGOTIATING ride past deadline")
		cancelled++
	}
	return cancelled, nil
}
