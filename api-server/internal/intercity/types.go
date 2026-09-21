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
	"strings"
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

// MinIntercityVehicleSeats is the smallest passenger capacity a vehicle may
// have and still publish on a corridor.
//
// Four is the CAB_TAXI line in the catalogue, and it is where the catalogue
// splits into vehicles that can and cannot do a two-hour highway journey:
// MOTO_BIKE(1) and HEAVY_FUSO(1, cargo) and TUK_TUK(3) fall below it,
// CAB_TAXI(4), LIGHT_HILUX(6), HIACE(8) and COASTER(18) clear it. Without this
// floor, `total_seats <= vehicle capacity` alone lets a moto publish a 1-seat
// "intercity trip" and sell a passenger a Kigali -> Musanze ride on the back of
// a motorcycle — a safety and regulatory problem, not a UX one: tuk-tuks and
// motos are not licensed for intercity transport and have no highway business.
//
// It is a floor on CAPACITY, not on the seats being sold: an eligible vehicle
// may still publish a single seat.
const MinIntercityVehicleSeats = 4

// VehicleSeatCapacity is the seat count an intercity publish may sell against:
// the driver's declared per-vehicle seats when they have one, otherwise the
// catalogue capacity of the vehicle type.
//
// The type ceiling is the fallback because the alternative (a fixed 30, the
// table's structural bound) let a cab whose owner never filled in
// passenger_seats publish thirty seats.
func VehicleSeatCapacity(declaredSeats *int, typeMaxPassengers int) int {
	if declaredSeats != nil && *declaredSeats > 0 {
		return *declaredSeats
	}
	if typeMaxPassengers > 0 {
		return typeMaxPassengers
	}
	return 1
}

// VehicleEligible reports whether a vehicle may publish intercity trips.
//
// Both the vehicle TYPE and the actual vehicle must clear the floor. The type
// is the regulatory category, so a MOTO_BIKE whose owner typed "6" into
// passenger_seats is still a moto; and a cab that declares two seats genuinely
// cannot seat four. Same predicate the publish path enforces and the driver
// vehicle list reports, so the screen a driver can open and the call that
// succeeds are never out of step.
func VehicleEligible(declaredSeats *int, typeMaxPassengers int) bool {
	return typeMaxPassengers >= MinIntercityVehicleSeats &&
		VehicleSeatCapacity(declaredSeats, typeMaxPassengers) >= MinIntercityVehicleSeats
}

// ErrSeatsUnavailable is returned when a seat acquisition wins no row. It
// deliberately conflates several causes — sold out, trip not OPEN, past the
// cutoff, soft-deleted, unknown id, or clamped by the driver's credit balance —
// because distinguishing them to the caller would leak a rival driver's
// inventory and balance. Classify separately for the user-facing message only.
var ErrSeatsUnavailable = errors.New("intercity: seats unavailable")

// ErrBookingNotFound is returned when a booking does not exist OR is not owned
// by the caller. The two are deliberately indistinguishable so the endpoint
// cannot be used to probe for other customers' booking ids.
var ErrBookingNotFound = errors.New("intercity: booking not found")

// ErrTripNotFound is returned for a missing or soft-deleted trip.
var ErrTripNotFound = errors.New("intercity: trip not found")

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

// DriftError reports trips whose seat counters no longer agree with their
// bookings, so a hold could not be released. It is a report, never a repair:
// seat counters are inventory, and a background worker silently "fixing" them
// would destroy the evidence of whatever caused the drift.
type DriftError struct{ TripIDs []string }

func (e *DriftError) Error() string {
	return "intercity: seat counter drift on trips " + strings.Join(e.TripIDs, ", ")
}

// ObligationError names trips whose credit obligation could not be recorded.
// Reported rather than swallowed: an unbillable trip that fails silently on
// every tick is revenue disappearing with no trace.
type ObligationError struct{ TripIDs []string }

func (e *ObligationError) Error() string {
	return "intercity: could not record obligations for trips " + strings.Join(e.TripIDs, ", ")
}
