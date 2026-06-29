package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/workspace/ride-platform/pkg/respond"
)

// BodyLimit caps the request body to maxBytes to prevent memory-exhaustion via
// oversized payloads. It wraps r.Body in an http.MaxBytesReader, so a handler
// reading the body gets an error once the cap is exceeded (and the server sends
// 413). Routes that legitimately stream large bodies (file uploads) must be
// excluded — see SkipPaths.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && maxBytes > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SkipPaths returns mw wrapped so it is bypassed for requests whose path has any
// of the given prefixes. Useful for exempting long-lived WebSocket upgrades and
// health checks from the global rate limiter, and large-upload routes from the
// global body cap.
func SkipPaths(mw func(http.Handler) http.Handler, prefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range prefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// SwaggerGate guards the API docs. When enabled is false the docs return 404.
// When basicAuth is "user:pass" it requires matching HTTP Basic credentials
// (constant-time compared) so the API surface isn't world-readable in prod.
func SwaggerGate(enabled bool, basicAuth string) func(http.Handler) http.Handler {
	user, pass, hasAuth := strings.Cut(basicAuth, ":")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled {
				respond.ErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "not found")
				return
			}
			if hasAuth && basicAuth != "" {
				u, p, ok := r.BasicAuth()
				userOK := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
				passOK := subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
				if !ok || !userOK || !passOK {
					w.Header().Set("WWW-Authenticate", `Basic realm="API docs"`)
					respond.ErrorMsg(w, http.StatusUnauthorized, "UNAUTHORIZED", "auth required")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
