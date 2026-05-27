package matching

// queue.go manages the driver accept/decline signal channels used
// by the matching engine goroutine loop.
// The actual channel store is in Engine.acceptChannels (sync.Map).
// This file exposes helper methods for the ride accept/decline handlers.

// AcceptRide is called by the driver accept handler to signal the matching goroutine.
// Returns true if the signal was delivered (goroutine still waiting), false if it timed out.
func (e *Engine) AcceptRide(rideID string) bool {
	return e.NotifyAccept(rideID, true)
}

// DeclineRide is called by the driver decline handler.
func (e *Engine) DeclineRide(rideID string) bool {
	return e.NotifyAccept(rideID, false)
}
