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
	UserID    string `json:"user_id"`
	RoleState string `json:"role_state"`
	TokenType string `json:"token_type"` // "access" | "refresh"
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
			header := r.Header.Get("Authorization")
			if header == "" || !strings.HasPrefix(header, "Bearer ") {
				respond.Error(w, apperrors.ErrUnauthorized)
				return
			}

			tokenStr := strings.TrimPrefix(header, "Bearer ")

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

			// Check Redis session liveness — catches revoked/logged-out tokens.
			jti := claims.ID
			if jti != "" {
				key := rkeys.K.Session(claims.UserID, jti)
				val, redisErr := rdb.Get(r.Context(), key).Result()
				if redisErr != nil || val == "revoked" {
					respond.Error(w, apperrors.ErrTokenRevoked)
					return
				}
			}

			ctx := context.WithValue(r.Context(), ContextKeyClaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
