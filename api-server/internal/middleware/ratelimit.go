package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// OTPRateLimit enforces a limit on OTP send/verify requests, keyed by the phone
// number in the REQUEST BODY (`phone_number`) — the actual OTP target.
//
// SECURITY: it must NOT key on a client-settable header. The body phone is what
// receives the SMS; if we keyed on a header an attacker could vary the header to
// get a fresh bucket each request and SMS-bomb the victim whose number is in the
// body (and bypass the limit entirely by omitting it).
//
// `prefix` separates buckets (e.g. "otp_send" vs "otp_verify"). Fails CLOSED:
// because these are SMS-cost / brute-force surfaces, a Redis error denies the
// request rather than allowing uncapped abuse.
func OTPRateLimit(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			phone := ""
			if r.Body != nil {
				body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				if err == nil {
					// Restore the body so the downstream handler can read it.
					r.Body = io.NopCloser(bytes.NewReader(body))
					var p struct {
						PhoneNumber string `json:"phone_number"`
					}
					_ = json.Unmarshal(body, &p)
					phone = strings.TrimSpace(p.PhoneNumber)
				}
			}

			var key string
			if phone != "" {
				key = fmt.Sprintf("ratelimit:%s:phone:%s", prefix, phone)
			} else {
				// No phone in body — still cap by IP so a malformed request can't bypass.
				key = fmt.Sprintf("ratelimit:%s:ip:%s", prefix, TrustedIP(r))
			}

			count, err := atomicIncr(r.Context(), rdb, key, window)
			if err != nil {
				// Fail closed on an SMS-cost / brute-force endpoint.
				respond.Error(w, apperrors.ErrRateLimited)
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
// Uses TrustedIP() which only reads proxy headers when the connection
// comes from a known private/loopback address, preventing header spoofing.
func IPRateLimit(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := TrustedIP(r)
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

// UserRateLimit429 is like UserRateLimit but returns 429 (not a silent 204) when
// the limit is exceeded. Use it as a per-user BACKSTOP on whole route groups,
// where the caller must know the request was rejected — unlike a droppable GPS
// ping. Keyed on JWT user_id, so it is immune to shared carrier-NAT IPs.
func UserRateLimit429(rdb *redis.Client, prefix string, maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				// No JWT (shouldn't reach here behind Authenticate) — pass through.
				next.ServeHTTP(w, r)
				return
			}
			key := fmt.Sprintf("ratelimit:user:%s:%s", prefix, claims.UserID)
			count, err := atomicIncr(r.Context(), rdb, key, window)
			if err != nil {
				// Redis unavailable — fail open rather than block real users.
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

// TrustedIP returns the real client IP.
//
// Security: X-Forwarded-For and X-Real-IP can be spoofed by any client.
// We only trust them when the direct connection comes from a private/loopback
// address (i.e. a known reverse proxy like Railway's edge or a local nginx).
// Direct public connections always use RemoteAddr so clients cannot fake their IP.
func TrustedIP(r *http.Request) string {
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
