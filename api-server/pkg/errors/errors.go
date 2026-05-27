package errors

import (
	"fmt"
	"net/http"
)

// AppError is a structured, HTTP-aware error type returned by service and
// repository layers. Handlers translate these into JSON responses.
type AppError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Sentinel errors — compare with errors.Is or direct equality.
var (
	ErrUnauthorized = &AppError{StatusCode: http.StatusUnauthorized, Code: "UNAUTHORIZED", Message: "authentication required"}
	ErrForbidden    = &AppError{StatusCode: http.StatusForbidden, Code: "FORBIDDEN", Message: "access denied"}
	ErrNotFound     = &AppError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND", Message: "resource not found"}
	ErrConflict     = &AppError{StatusCode: http.StatusConflict, Code: "CONFLICT", Message: "resource already exists"}
	ErrBadRequest   = &AppError{StatusCode: http.StatusBadRequest, Code: "BAD_REQUEST", Message: "invalid request"}
	ErrInternal     = &AppError{StatusCode: http.StatusInternalServerError, Code: "INTERNAL", Message: "internal server error"}

	// Auth
	ErrInvalidOTP       = &AppError{StatusCode: http.StatusBadRequest, Code: "INVALID_OTP", Message: "OTP is invalid or expired"}
	ErrOTPExpired       = &AppError{StatusCode: http.StatusBadRequest, Code: "OTP_EXPIRED", Message: "OTP has expired"}
	ErrOTPAlreadyUsed   = &AppError{StatusCode: http.StatusBadRequest, Code: "OTP_ALREADY_USED", Message: "OTP has already been used"}
	ErrTokenExpired     = &AppError{StatusCode: http.StatusUnauthorized, Code: "TOKEN_EXPIRED", Message: "token has expired"}
	ErrTokenRevoked     = &AppError{StatusCode: http.StatusUnauthorized, Code: "TOKEN_REVOKED", Message: "session has been revoked"}
	ErrTokenInvalid     = &AppError{StatusCode: http.StatusUnauthorized, Code: "TOKEN_INVALID", Message: "token is invalid"}
	ErrRateLimited      = &AppError{StatusCode: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "too many requests, please wait"}

	// Driver
	ErrDriverNotActive       = &AppError{StatusCode: http.StatusForbidden, Code: "DRIVER_NOT_ACTIVE", Message: "driver profile is not active"}
	ErrDriverAlreadyApplied  = &AppError{StatusCode: http.StatusConflict, Code: "DRIVER_ALREADY_APPLIED", Message: "driver application already submitted"}
	ErrSelfApproval          = &AppError{StatusCode: http.StatusForbidden, Code: "SELF_APPROVAL", Message: "admin cannot approve their own driver application"}
	ErrDriverOfflineCooldown = &AppError{StatusCode: http.StatusForbidden, Code: "DRIVER_OFFLINE_COOLDOWN", Message: "offline penalty still active"}

	// Ride
	ErrRideNotFound          = &AppError{StatusCode: http.StatusNotFound, Code: "RIDE_NOT_FOUND", Message: "ride not found"}
	ErrInvalidTransition     = &AppError{StatusCode: http.StatusConflict, Code: "INVALID_TRANSITION", Message: "ride state transition not allowed"}
	ErrFareLocked            = &AppError{StatusCode: http.StatusConflict, Code: "FARE_LOCKED", Message: "agreed fare cannot be modified"}
	ErrGeoFence              = &AppError{StatusCode: http.StatusUnprocessableEntity, Code: "GEO_FENCE_VIOLATION", Message: "driver is not within required radius"}
	ErrAcceptExpired         = &AppError{StatusCode: http.StatusConflict, Code: "ACCEPT_EXPIRED", Message: "ride request has expired"}
	ErrCustomerSuspended     = &AppError{StatusCode: http.StatusForbidden, Code: "CUSTOMER_SUSPENDED", Message: "booking is temporarily suspended"}
	ErrNegotiationRoundLimit = &AppError{StatusCode: http.StatusConflict, Code: "NEGOTIATION_ROUND_LIMIT", Message: "maximum negotiation rounds reached"}

	// GPS
	ErrGPSPlausibility = &AppError{StatusCode: http.StatusUnprocessableEntity, Code: "GPS_PLAUSIBILITY", Message: "GPS update rejected: speed implausible"}
	ErrGPSCoords       = &AppError{StatusCode: http.StatusBadRequest, Code: "GPS_INVALID_COORDS", Message: "GPS coordinates out of valid range"}
)

// New creates a transient AppError (not a sentinel — used for dynamic messages).
func New(statusCode int, code, message string) *AppError {
	return &AppError{StatusCode: statusCode, Code: code, Message: message}
}

// Newf creates a transient AppError with a formatted message.
func Newf(statusCode int, code, format string, args ...interface{}) *AppError {
	return &AppError{StatusCode: statusCode, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Is enables errors.Is comparisons on AppError sentinels.
func (e *AppError) Is(target error) bool {
	t, ok := target.(*AppError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}
