package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
	"github.com/workspace/ride-platform/pkg/respond"
)

type contextKey string

const (
	ContextKeyClaims contextKey = "claims"
	ContextKeyLogger contextKey = "logger"
)

// Claims are the JWT payload fields embedded in every access token.
type Claims struct {
	UserID      string `json:"user_id"`
	RoleState   string `json:"role_state"`
	TokenType   string `json:"token_type"`   // "access" | "refresh"
	AdminRole   string `json:"admin_role"`   // set only for admin tokens: SUPER_ADMIN, OPS_MANAGER, etc.
	IsSuspended bool   `json:"is_suspended"` // embedded so suspension is enforced without a DB hit
	jwt.RegisteredClaims
}

// GetClaims extracts JWT claims from the request context.
func GetClaims(r *http.Request) *Claims {
	c, _ := r.Context().Value(ContextKeyClaims).(*Claims)
	return c
}

// GetLogger retrieves the zerolog.Logger injected by WithLogger.
func GetLogger(r *http.Request) zerolog.Logger {
	l, ok := r.Context().Value(ContextKeyLogger).(zerolog.Logger)
	if !ok {
		return zerolog.Nop()
	}
	return l
}

// WithLogger injects the root logger into every request context.
func WithLogger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ContextKeyLogger, log)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticate validates the Bearer JWT and checks session liveness in Redis.
// Role enforcement is done separately by RequireRole middleware.
func Authenticate(cfg *config.Config, rdb *goredis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := ""
			if header := r.Header.Get("Authorization"); header != "" && strings.HasPrefix(header, "Bearer ") {
				tokenStr = strings.TrimPrefix(header, "Bearer ")
			} else if q := r.URL.Query().Get("token"); q != "" {
				// SECURITY TODO: Implement a short-lived, single-use ticket exchange mechanism for WS
				// authentication instead of exposing the long-lived JWT in URL query parameters.
				// Mobile WebSocket clients cannot set Authorization headers; they pass JWT via query.
				tokenStr = q
			}
			if tokenStr == "" {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}

			claims := &Claims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, apperrors.ErrTokenInvalid
				}
				return []byte(cfg.JWT.AccessSecret), nil
			})

			if err != nil || !token.Valid {
				if err != nil && strings.Contains(err.Error(), "expired") {
					respond.Error(w, apperrors.ErrTokenExpired)
					return
				}
				respond.Error(w, apperrors.ErrTokenInvalid)
				return
			}

			if claims.TokenType != "access" {
				respond.Error(w, apperrors.ErrTokenInvalid)
				return
			}

			// Check Redis session liveness — catches revoked/logged-out tokens.
			jti := claims.ID
			if jti == "" {
				respond.Error(w, apperrors.ErrTokenInvalid)
				return
			}
			key := rkeys.K.Session(claims.UserID, jti)
			val, redisErr := rdb.Get(r.Context(), key).Result()
			if redisErr != nil || val != "valid" {
				respond.Error(w, apperrors.ErrTokenRevoked)
				return
			}

			ctx := context.WithValue(r.Context(), ContextKeyClaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
