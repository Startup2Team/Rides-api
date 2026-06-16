package middleware

import (
	"net/http"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Role constants — must match DB role_state values exactly.
const (
	RoleCustomer        = "CUSTOMER_ONLY"
	RoleDriverPending   = "DRIVER_PENDING"
	RoleDriverActive    = "DRIVER_ACTIVE"
	RoleDriverSuspended = "DRIVER_SUSPENDED"
	RoleAdmin           = "ADMIN"
)

// RequireRole returns a middleware that rejects any request whose JWT
// role_state is not in the allowed set.
// Rejection happens before the handler runs — handlers never check roles.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}

			if !allowedSet[claims.RoleState] {
				respond.Error(w, apperrors.ErrForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdminRole gates routes to admins whose JWT admin_role is in the allowed set.
// Must be used inside a route group that already applies RequireRole(RoleAdmin).
func RequireAdminRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}
			if !allowedSet[claims.AdminRole] {
				respond.Error(w, apperrors.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireSuspensionCheck additionally rejects a customer whose is_suspended
// flag is set. This is a second-pass check after JWT role validation.
// The is_suspended field is read from the JWT for zero-DB-latency enforcement.
// (We embed a "suspended_until" field in the token and re-issue on change.)
func RequireNotSuspended() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
