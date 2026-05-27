package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// OTPRateLimit enforces a per-phone limit on OTP send requests.
// Allows `maxRequests` per `window`. Key is passed by the caller so the
// limit can be applied to any dimension (phone, IP, user_id, etc.).
func OTPRateLimit(rdb *redis.Client, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Caller must set X-Rate-Limit-Key header before this middleware runs,
			// or we extract phone from body. We use a simple Redis INCR + EXPIRE approach.
			phone := r.Header.Get("X-Phone-Number")
			if phone == "" {
				next.ServeHTTP(w, r)
				return
			}

			key := fmt.Sprintf("ratelimit:otp:%s", phone)
			count, err := rdb.Incr(context.Background(), key).Result()
			if err != nil {
				// Redis down — allow request through (fail open for OTP)
				next.ServeHTTP(w, r)
				return
			}

			if count == 1 {
				// First request in window — set expiry
				rdb.Expire(context.Background(), key, window)
			}

			if count > int64(maxRequests) {
				respond.Error(w, apperrors.ErrRateLimited)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// IPRateLimit is a general per-IP rate limiter backed by Redis.
func IPRateLimit(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			key := fmt.Sprintf("ratelimit:%s:%s", prefix, ip)

			count, err := rdb.Incr(context.Background(), key).Result()
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			if count == 1 {
				rdb.Expire(context.Background(), key, window)
			}

			if count > int64(maxRequests) {
				respond.Error(w, apperrors.ErrRateLimited)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
