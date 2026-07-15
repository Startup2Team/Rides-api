package matching

// queue.go manages the driver accept/decline signal channels used
// by the matching engine goroutine loop.
// The actual channel store is in Engine.acceptChannels (sync.Map).
// This file exposes helper methods for the ride accept/decline handlers.

// AcceptRide is called by the driver accept handler to signal the matching
// goroutine. driverID is the accepting driver's profile id, verified against
// the current offeree by the matching loop. Returns true if the signal was
// delivered (goroutine still waiting), false if it timed out.
func (e *Engine) AcceptRide(rideID, driverID string) bool {
	return e.NotifyAccept(rideID, driverID, true)
}

// DeclineRide is called by the driver decline handler.
func (e *Engine) DeclineRide(rideID, driverID string) bool {
	return e.NotifyAccept(rideID, driverID, false)
}
