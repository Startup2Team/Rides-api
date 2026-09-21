// Package intercity implements scheduled multi-seat intercity trips: a driver
// publishes a route with seats and a price, and passengers book seats against
// live availability.
//
// It is a separate product line from point-to-point rides and does not reuse
// the rides table, which is strictly 1:1 (one customer, one driver). It also
// never invokes the matching engine — passengers choose a driver from a list.
// The only resource shared with city rides is driver availability, gated by
// driver_profiles.intercity_committed_until.
//
// Design: /Users/paccee/Pac/Rides/INTERCITY_DESIGN.md (v3)
package intercity

import (
	"errors"
	"time"
)

// Trip statuses. PARTIALLY BOOKED and FULL are deliberately absent: fullness is
// DERIVED from the seat counters at read time. Storing it would create a second
// source of truth, and a stale FULL strands passengers.
const (
	TripOpen      = "OPEN"
	TripBoarding  = "BOARDING"
	TripInTransit = "IN_TRANSIT"
	TripCompleted = "COMPLETED"
	TripCancelled = "CANCELLED"
)

// Booking statuses. CONFIRMED and BOARDED are disjoint: a seat that has boarded
// is no longer CONFIRMED, which is why anything counting "seats sold" must sum
// the cumulative cohort rather than either status alone.
const (
	BookingHeld      = "HELD"
	BookingConfirmed = "CONFIRMED"
	BookingBoarded   = "BOARDED"
	BookingCompleted = "COMPLETED"
	BookingExpired   = "EXPIRED"
	BookingCancelled = "CANCELLED"
	BookingNoShow    = "NO_SHOW"
)

// HoldTTL is how long a held seat survives without confirmation. Kept short
// because a hold costs the passenger nothing: there is no payment step at
// booking, so a generous window is inventory given away for free.
const HoldTTL = 5 * time.Minute

// BookingCutoff is how close to departure a seat may still be taken.
const BookingCutoff = 5 * time.Minute

// ErrSeatsUnavailable is returned when a seat acquisition wins no row. It
// deliberately conflates several causes — sold out, trip not OPEN, past the
// cutoff, soft-deleted, unknown id, or clamped by the driver's credit balance —
// because distinguishing them to the caller would leak a rival driver's
// inventory and balance. Classify separately for the user-facing message only.
var ErrSeatsUnavailable = errors.New("intercity: seats unavailable")

// Booking is a passenger's claim on seats in a trip.
type Booking struct {
	ID            string
	TripID        string
	CustomerID    string
	Seats         int
	Status        string
	PricePerSeat  int
	HoldExpiresAt *time.Time
	// Remaining is the seats left on the trip immediately after this operation.
	// Informational only — never authoritative for a subsequent decision.
	Remaining int
}
