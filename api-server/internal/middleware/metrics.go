package middleware

import (
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

type RouteMetrics struct {
	Count    uint64
	Duration uint64 // total duration in milliseconds
}

type MetricsCollector struct {
	mu            sync.RWMutex
	routeMetrics  map[string]*RouteMetrics
	totalRequests uint64
}

var GlobalMetrics = &MetricsCollector{
	routeMetrics: make(map[string]*RouteMetrics),
}

var uuidRegex = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func sanitizePath(path string) string {
	return uuidRegex.ReplaceAllString(path, "{id}")
}

func (m *MetricsCollector) Record(method, path string, duration time.Duration) {
	atomic.AddUint64(&m.totalRequests, 1)

	pathKey := sanitizePath(path)
	key := method + " " + pathKey

	m.mu.Lock()
	rm, ok := m.routeMetrics[key]
	if !ok {
		rm = &RouteMetrics{}
		m.routeMetrics[key] = rm
	}
	m.mu.Unlock()

	atomic.AddUint64(&rm.Count, 1)
	atomic.AddUint64(&rm.Duration, uint64(duration.Milliseconds()))
}

func (m *MetricsCollector) GetMetrics() (uint64, map[string]RouteMetrics) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string]RouteMetrics)
	for k, v := range m.routeMetrics {
		res[k] = RouteMetrics{
			Count:    atomic.LoadUint64(&v.Count),
			Duration: atomic.LoadUint64(&v.Duration),
		}
	}
	return atomic.LoadUint64(&m.totalRequests), res
}

func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)

		GlobalMetrics.Record(r.Method, r.URL.Path, duration)
	})
}
