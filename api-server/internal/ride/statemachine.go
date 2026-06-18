package ride

import (
	"fmt"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Status mirrors the DB ride status values exactly.
type Status string

const (
	StatusSearching     Status = "SEARCHING"
	StatusMatched       Status = "MATCHED"
	StatusNegotiating   Status = "NEGOTIATING"
	StatusConfirmed     Status = "CONFIRMED"
	StatusDriverEnRoute Status = "DRIVER_EN_ROUTE"
	StatusDriverArrived Status = "DRIVER_ARRIVED"
	StatusInProgress    Status = "IN_PROGRESS"
	StatusCompleted     Status = "COMPLETED"
	StatusCancelled     Status = "CANCELLED"
)

// allowedTransitions is the authoritative state machine definition.
// Any transition not listed here is rejected.
var allowedTransitions = map[Status]map[Status]bool{
	StatusSearching: {
		StatusMatched:   true,
		StatusCancelled: true,
	},
	StatusMatched: {
		StatusNegotiating: true,
		StatusSearching:   true, // driver declined at match stage
	},
	StatusNegotiating: {
		StatusConfirmed: true,
		StatusSearching: true, // negotiation failed — find next driver
		StatusCancelled: true, // customer cancelled during negotiation
	},
	StatusConfirmed: {
		StatusDriverEnRoute: true,
		StatusCancelled:     true, // driver cancelled after confirming (before going en-route)
	},
	StatusDriverEnRoute: {
		StatusDriverArrived: true,
		StatusCancelled:     true, // customer or driver cancelled while driver is en-route
	},
	StatusDriverArrived: {
		StatusInProgress: true, // driver submits Start Ride
		StatusCancelled:  true, // customer cancel after arrival, or driver no-show cancel
	},
	StatusInProgress: {
		StatusCompleted: true, // driver submits Complete Ride
	},
	// COMPLETED and CANCELLED are terminal — no outgoing transitions.
}

// ErrInvalidTransition contains the attempted from/to for structured logging.
type ErrInvalidTransition struct {
	From Status
	To   Status
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid ride transition: %s → %s", e.From, e.To)
}

// CanTransition returns true if the transition from → to is allowed.
func CanTransition(from, to Status) bool {
	allowed, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// ValidateTransition returns nil if allowed, or a typed AppError if not.
func ValidateTransition(from, to Status) error {
	if !CanTransition(from, to) {
		return apperrors.ErrInvalidTransition
	}
	return nil
}

// CancellableStatuses are states in which a customer may cancel.
// Cancellations during DRIVER_EN_ROUTE carry no fee; during DRIVER_ARRIVED
// a late-cancel fee may apply (computed in CancelRide based on fare config).
var CancellableStatuses = map[Status]bool{
	StatusSearching:     true,
	StatusMatched:       true,
	StatusNegotiating:   true,
	StatusDriverEnRoute: true, // customer can cancel while driver is en-route
	StatusDriverArrived: true, // customer can cancel after driver arrives (fee may apply)
}

// IsTerminal returns true if the status has no outgoing transitions.
func IsTerminal(s Status) bool {
	return s == StatusCompleted || s == StatusCancelled
}
