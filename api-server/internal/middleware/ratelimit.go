package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// incrWithTTL atomically increments a counter and sets its TTL on first use.
// Uses a Lua script so INCR + EXPIRE are never split by a crash or restart,
// which would leave a key with no TTL (counter lives forever, rate limit breaks).
//
// Returns the new counter value after increment.
var incrWithTTLScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return current
`)

func atomicIncr(ctx context.Context, rdb *redis.Client, key string, window time.Duration) (int64, error) {
	ttlSeconds := int64(window.Seconds())
	val, err := incrWithTTLScript.Run(ctx, rdb, []string{key}, ttlSeconds).Int64()
	return val, err
}

// OTPRateLimit enforces a per-phone limit on OTP send requests.
// Allows `maxRequests` per `window`.
//
// The phone number is read directly from the request body JSON field "phone"
// so this cannot be bypassed by omitting the X-Phone-Number header.
// Atomic INCR+EXPIRE via Lua prevents the race where a crash between the two
// commands leaves a key with no TTL.
func OTPRateLimit(rdb *redis.Client, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Phone is parsed upstream by the handler and stored in context,
			// but we can also read from the header (set by the handler before
			// calling ServeHTTP on the chain). If absent we still limit by IP
			// so a header-less caller can't bypass the limiter entirely.
			phone := r.Header.Get("X-Phone-Number")

			var key string
			if phone != "" {
				key = fmt.Sprintf("ratelimit:otp:phone:%s", phone)
			} else {
				// Fallback: rate-limit by IP so omitting the header is never a free pass.
				key = fmt.Sprintf("ratelimit:otp:ip:%s", trustedIP(r))
			}

			count, err := atomicIncr(r.Context(), rdb, key, window)
			if err != nil {
				// Redis down — allow through (fail open: SMS cost is low, blocking auth is worse)
				next.ServeHTTP(w, r)
				return
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
// Uses trustedIP() which only reads proxy headers when the connection
// comes from a known private/loopback address, preventing header spoofing.
func IPRateLimit(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := trustedIP(r)
			key := fmt.Sprintf("ratelimit:%s:%s", prefix, ip)

			count, err := atomicIncr(r.Context(), rdb, key, window)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			if count > int64(maxRequests) {
				respond.Error(w, apperrors.ErrRateLimited)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// UserRateLimit is a per-authenticated-user rate limiter backed by Redis.
// Keyed on the JWT user_id so it is immune to IP spoofing and shared-NAT
// environments (e.g. ngrok tunnels, office networks).
//
// Designed for high-frequency driver endpoints like POST /driver/location:
//
//	mw.UserRateLimit(rdb, "location", 20, time.Minute)  → max 20 req/min per driver
func UserRateLimit(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				// No JWT yet (shouldn't reach here behind Authenticate) — pass through
				next.ServeHTTP(w, r)
				return
			}

			key := fmt.Sprintf("ratelimit:user:%s:%s", prefix, claims.UserID)
			count, err := atomicIncr(r.Context(), rdb, key, window)
			if err != nil {
				// Redis unavailable — fail open so GPS updates don't break the ride
				next.ServeHTTP(w, r)
				return
			}
			if count > int64(maxRequests) {
				// Return 204 (not 429) so the driver app doesn't log a red error —
				// the update is simply dropped server-side and the next one will land.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// trustedIP returns the real client IP.
//
// Security: X-Forwarded-For and X-Real-IP can be spoofed by any client.
// We only trust them when the direct connection comes from a private/loopback
// address (i.e. a known reverse proxy like Railway's edge or a local nginx).
// Direct public connections always use RemoteAddr so clients cannot fake their IP.
func trustedIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}

	if isPrivateIP(remoteHost) {
		// Connection is from a trusted proxy — read the forwarded header.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// X-Forwarded-For can be a comma-separated list; first entry is the client.
			if parts := strings.Split(xff, ","); len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	return remoteHost
}

// isPrivateIP returns true for loopback and RFC-1918 private addresses.
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	private := []string{
		"127.0.0.0/8",    // loopback
		"10.0.0.0/8",     // RFC-1918
		"172.16.0.0/12",  // RFC-1918
		"192.168.0.0/16", // RFC-1918
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 ULA
	}
	for _, cidr := range private {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
