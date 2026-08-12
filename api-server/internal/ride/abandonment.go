package ride

import (
	"context"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

	"github.com/workspace/ride-platform/internal/tracking"
)

// Abandonment watchdog.
//
// A driver who kills the app after a fare is agreed leaves the ride frozen in
// CONFIRMED / DRIVER_EN_ROUTE / DRIVER_ARRIVED forever: none of those states
// had any timeout, so the customer sat staring at a driver who was never
// coming, and the driver walked away with no penalty. This job is those
// states' missing dead-man switch, run on a periodic tick from main.
//
// The cancel is deliberately DRIVER-fault with the same consequences as
// DriverCancelRide: the agreed-fare credit stays forfeited (no refund) and the
// escalating cancel penalty is recorded. Abandoning by going dark must not be
// cheaper than tapping Cancel.

// abandonThreshold returns how long a ride in this status may sit without any
// state change before a silent driver is treated as having abandoned it.
// ok=false means the status is not covered by the watchdog.
func abandonThreshold(status Status, active, onboard time.Duration) (time.Duration, bool) {
	switch status {
	case StatusConfirmed, StatusDriverEnRoute, StatusDriverArrived:
		return active, true
	case StatusInProgress:
		// A passenger is (nominally) aboard: tunnels and dead zones mid-trip are
		// normal, so the window is longer, and the dead-man finalizer still
		// backstops anything this misses.
		return onboard, true
	}
	return 0, false
}

// eligibleForAbandonCancel is the watchdog's pure decision. Cancelling a live
// ride on someone's behalf is drastic, so every condition must hold:
//
//   - the ride has sat in a covered status past its threshold (updated_at is
//     bumped on every transition, so this is "nothing has happened for N min");
//   - the driver's location key has expired (it carries a 120s TTL refreshed on
//     every GPS fix — mere existence proves a recent fix);
//   - AND the driver holds no live WebSocket on any replica.
//
// Requiring both silence signals on top of the stall means a driver who is
// simply between GPS ticks, or whose location write hiccuped, is never
// mistaken for an abandoner.
func eligibleForAbandonCancel(status Status, updatedAt, now time.Time, active, onboard time.Duration, locationFresh, socketConnected bool) bool {
	threshold, ok := abandonThreshold(status, active, onboard)
	if !ok {
		return false
	}
	if now.Sub(updatedAt) < threshold {
		return false
	}
	return !locationFresh && !socketConnected
}

// driverSilent reports whether the driver is dark right now: location key
// expired AND no live socket. A Redis read error counts as "not silent" — the
// watchdog must fail safe and never cancel a ride on a Redis blip.
func (s *Service) driverSilent(ctx context.Context, driverProfileID string) bool {
	n, err := s.redis.Exists(ctx, rkeys.K.DriverLocation(driverProfileID)).Result()
	if err != nil || n > 0 {
		return false
	}
	return !s.hub.IsDriverConnected(driverProfileID)
}

// CancelAbandonedRides scans live rides whose driver has gone dark and cancels
// them as driver-fault. Returns how many were cancelled. Designed to be called
// on a periodic background tick (see cmd/server/main.go).
func (s *Service) CancelAbandonedRides(ctx context.Context) (int, error) {
	activeMin := s.cfg.Ride.AbandonSilenceMinutes
	onboardMin := s.cfg.Ride.AbandonOnboardSilenceMinutes
	candidates, err := s.repo.FindAbandonCandidates(ctx, activeMin, onboardMin)
	if err != nil {
		return 0, err
	}

	active := time.Duration(activeMin) * time.Minute
	onboard := time.Duration(onboardMin) * time.Minute
	cancelled := 0
	for _, c := range candidates {
		locationFresh := true
		if n, lerr := s.redis.Exists(ctx, rkeys.K.DriverLocation(c.DriverProfileID)).Result(); lerr == nil {
			locationFresh = n > 0
		}
		if !eligibleForAbandonCancel(c.Status, c.UpdatedAt, time.Now(), active, onboard,
			locationFresh, s.hub.IsDriverConnected(c.DriverProfileID)) {
			continue
		}

		// Atomic — a driver coming back to life or a concurrent cancel between
		// the scan and here loses the race cleanly.
		didCancel, cerr := s.repo.Cancel(ctx, c.ID, "driver abandoned the ride", "SYSTEM")
		if cerr != nil || !didCancel {
			continue
		}

		// Same consequences as DriverCancelRide: NO credit refund (the agreed-fare
		// credit stays forfeited) and the escalating cancel penalty. For an
		// IN_PROGRESS abandonment the customer is charged nothing — no final fare,
		// no waiting charge; the cancel above records no fee.
		s.recordCancelPenalty(ctx, c.DriverUserID, "DRIVER")
		profileID := c.DriverProfileID
		s.releaseRideRedisState(ctx, c.ID, c.CustomerID, &profileID, c.TransportType)

		_ = s.repo.AppendEvent(ctx, c.ID, "ride.cancelled", "SYSTEM", c.ID, map[string]interface{}{
			"reason":           "driver_abandoned",
			"status_at_cancel": string(c.Status),
			"silence_minutes":  int(time.Since(c.UpdatedAt).Minutes()),
		})
		s.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", c.ID, &c.ID, map[string]interface{}{
			"ride_id": c.ID, "reason": "driver_abandoned",
			"cancelled_by_role": "SYSTEM", "status_at_cancel": string(c.Status),
			"driver_id": c.DriverProfileID,
		})

		// Tell the customer on both transports — the whole point is that nobody
		// is coming, so their app may well be backgrounded by now.
		s.hub.SendToCustomer(c.ID, tracking.Message{
			Type: "ride_cancelled", RideID: c.ID,
			Payload: map[string]interface{}{"reason": "Your driver is no longer available — you can book again."},
		})
		s.notify.SendToAllDevices(ctx, c.CustomerID, "Ride cancelled",
			"Your driver is no longer available — you can book again.", "ride",
			map[string]string{"type": "ride_cancelled", "ride_id": c.ID})

		s.log.Warn().Str("ride_id", c.ID).Str("driver_profile_id", c.DriverProfileID).
			Str("status_at_cancel", string(c.Status)).
			Msg("abandonment: cancelled ride — driver silent past threshold")
		cancelled++
	}
	return cancelled, nil
}

// CancelActiveRideForDriverExit driver-fault cancels whatever active ride the
// driver holds in CONFIRMED..IN_PROGRESS. Called by driver.ForceOffline (via
// the interface wired in main) before logout or account deletion tears down
// the driver's Redis state — without this, one tap of Logout cleared
// driver:<id>:active_ride and made walking out on an agreed ride penalty-free.
//
// Unlike DriverCancelRide it also covers IN_PROGRESS: a driver logging out
// with a passenger recorded aboard has abandoned the trip, and leaving the
// ride for the 2-hour finalizer would have paid them the full fare for it.
func (s *Service) CancelActiveRideForDriverExit(ctx context.Context, driverUserID, reason string) error {
	r, err := s.repo.FindActiveByDriver(ctx, driverUserID)
	if err != nil {
		// No active ride — nothing to guard. A real lookup failure is logged but
		// never blocks the logout that triggered us.
		if err != apperrors.ErrRideNotFound {
			s.log.Warn().Err(err).Str("driver_user_id", driverUserID).
				Msg("ride: active-ride lookup failed during driver exit guard")
		}
		return nil
	}
	switch r.Status {
	case StatusConfirmed, StatusDriverEnRoute, StatusDriverArrived, StatusInProgress:
	default:
		// Pre-agreement states (MATCHED / NEGOTIATING) have their own timeouts
		// and no credit at stake yet.
		return nil
	}

	didCancel, err := s.repo.Cancel(ctx, r.ID, reason, "DRIVER")
	if err != nil {
		return err
	}
	if !didCancel {
		return nil
	}
	// Same semantics as DriverCancelRide: forfeited credit, escalating penalty.
	s.recordCancelPenalty(ctx, driverUserID, "DRIVER")
	s.releaseRideRedisState(ctx, r.ID, r.CustomerID, r.DriverID, r.TransportType)
	_ = s.repo.AppendEvent(ctx, r.ID, "ride.cancelled", "DRIVER", driverUserID, map[string]interface{}{
		"reason": reason, "status_at_cancel": string(r.Status),
	})
	s.analytics.Publish(ctx, "ride.cancelled", "DRIVER", driverUserID, &r.ID, map[string]interface{}{
		"ride_id": r.ID, "reason": reason,
		"cancelled_by_role": "DRIVER", "status_at_cancel": string(r.Status),
	})
	s.hub.SendToCustomer(r.ID, tracking.Message{
		Type: "ride_cancelled", RideID: r.ID,
		Payload: map[string]interface{}{"reason": "Your driver is no longer available — you can book again."},
	})
	s.notify.SendToAllDevices(ctx, r.CustomerID, "Ride cancelled",
		"Your driver is no longer available — you can book again.", "ride",
		map[string]string{"type": "ride_cancelled", "ride_id": r.ID})
	s.log.Warn().Str("ride_id", r.ID).Str("driver_user_id", driverUserID).
		Str("status_at_cancel", string(r.Status)).
		Msg("ride: driver-fault cancel on logout/exit with active ride")
	return nil
}
