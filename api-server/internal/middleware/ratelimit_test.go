package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// okHandler returns 200 for any request it reaches (i.e. was not rate-limited).
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func countCode(codes []int, want int) int {
	n := 0
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}

// fireUser sends n requests carrying userID's JWT claims through h.
func fireUser(h http.Handler, userID string, n int) []int {
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(context.WithValue(req.Context(), ContextKeyClaims, &Claims{UserID: userID}))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		codes[i] = rr.Code
	}
	return codes
}

// fireIP sends n requests from the given direct client IP through h.
func fireIP(h http.Handler, ip string, n int) []int {
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":10000"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		codes[i] = rr.Code
	}
	return codes
}

// The critical carrier-NAT test for the GROUP BACKSTOP (UserRateLimit429):
// two users behind the SAME IP have INDEPENDENT budgets, and over-limit returns
// a proper 429 (the caller knows it was rejected). User A exhausting their limit
// must NOT block user B. This is the fix that makes the authed groups NAT-proof.
func TestUserRateLimit429_IsolatesUsersOnSharedIP(t *testing.T) {
	rdb := newTestRedis(t)
	limit := 5
	h := UserRateLimit429(rdb, "test", limit, time.Minute)(okHandler)

	a := fireUser(h, "userA", limit+1)
	if ok := countCode(a, http.StatusOK); ok != limit {
		t.Fatalf("userA: want %d OK, got %d", limit, ok)
	}
	if a[limit] != http.StatusTooManyRequests {
		t.Fatalf("userA: request %d should be 429, got %d", limit+1, a[limit])
	}

	// User B shares the same IP but has a different JWT — must be unaffected.
	b := fireUser(h, "userB", limit)
	if ok := countCode(b, http.StatusOK); ok != limit {
		t.Fatalf("userB should be unaffected by userA hitting the limit: want %d OK, got %d", limit, ok)
	}
}

func TestUserRateLimit429_EnforcesPerUserLimit(t *testing.T) {
	rdb := newTestRedis(t)
	limit := 10
	h := UserRateLimit429(rdb, "test", limit, time.Minute)(okHandler)

	codes := fireUser(h, "u1", limit+3)
	if ok := countCode(codes, http.StatusOK); ok != limit {
		t.Fatalf("want %d OK, got %d", limit, ok)
	}
	if blocked := countCode(codes, http.StatusTooManyRequests); blocked != 3 {
		t.Fatalf("want 3 blocked (429), got %d", blocked)
	}
}

// The GPS-ping variant (UserRateLimit) deliberately DROPS silently with 204 so
// the driver app doesn't log errors — verify that behavior, and that it too is
// per-user isolated.
func TestUserRateLimit_Drops204_AndIsolatesUsers(t *testing.T) {
	rdb := newTestRedis(t)
	limit := 5
	h := UserRateLimit(rdb, "test", limit, time.Minute)(okHandler)

	a := fireUser(h, "userA", limit+1)
	if a[limit] != http.StatusNoContent {
		t.Fatalf("over-limit GPS ping should be silently dropped (204), got %d", a[limit])
	}
	b := fireUser(h, "userB", limit)
	if ok := countCode(b, http.StatusOK); ok != limit {
		t.Fatalf("userB should be unaffected: want %d OK, got %d", limit, ok)
	}
}

// Documents the failure mode we're avoiding: IP limiting throttles everyone
// sharing an IP, which is exactly what breaks legitimate users behind CGNAT.
func TestIPRateLimit_ThrottlesEveryoneSharingAnIP(t *testing.T) {
	rdb := newTestRedis(t)
	limit := 5
	h := IPRateLimit(rdb, "test", limit, time.Minute)(okHandler)

	codes := fireIP(h, "203.0.113.9", limit+2)
	if ok := countCode(codes, http.StatusOK); ok != limit {
		t.Fatalf("want %d OK before the shared IP is throttled, got %d", limit, ok)
	}
	if codes[limit] != http.StatusTooManyRequests {
		t.Fatalf("request past the IP limit should be 429, got %d", codes[limit])
	}
}

func TestIPRateLimit_DifferentIPsAreIndependent(t *testing.T) {
	rdb := newTestRedis(t)
	limit := 3
	h := IPRateLimit(rdb, "test", limit, time.Minute)(okHandler)

	_ = fireIP(h, "203.0.113.1", limit) // exhaust IP 1
	codes := fireIP(h, "203.0.113.2", limit)
	if ok := countCode(codes, http.StatusOK); ok != limit {
		t.Fatalf("IP 2 must be independent of IP 1: want %d OK, got %d", limit, ok)
	}
}
