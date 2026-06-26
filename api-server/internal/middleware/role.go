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

// RequireNotSuspended rejects any request from a suspended account.
// The is_suspended flag is embedded in the JWT — zero DB latency.
// Admins revoke the session via Redis when suspending a user, so even
// live tokens are blocked within one Redis TTL (access token lifetime).
func RequireNotSuspended() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}
			if claims.IsSuspended {
				respond.ErrorMsg(w, http.StatusForbidden, "ACCOUNT_SUSPENDED",
					"Your account has been suspended. Contact support.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
