package middleware

import (
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

// HTTPLogger logs every inbound HTTP request with method, path, status, and
// latency. Wire it after chimw.RequestID so the request ID is available.
func HTTPLogger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				status := ww.Status()
				dur := time.Since(start)

				ev := log.Info()
				if status >= 500 {
					ev = log.Error()
				} else if status >= 400 {
					ev = log.Warn()
				}

				ev.
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Int("status", status).
					Str("duration", dur.String()).
					Str("ip", r.RemoteAddr).
					Str("request_id", chimw.GetReqID(r.Context())).
					Msg("http")
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// SecurityHeaders injects standard web security headers (Helmet equivalents).
func SecurityHeaders(env string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "0")
			w.Header().Set("Referrer-Policy", "no-referrer")

			// Strict CSP for API server
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox;")

			if env == "production" {
				w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}

			next.ServeHTTP(w, r)
		})
	}
}
