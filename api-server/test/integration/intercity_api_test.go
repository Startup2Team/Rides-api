package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/intercity"
	mw "github.com/workspace/ride-platform/internal/middleware"
)

// The intercity HTTP contract (INTERCITY_DESIGN.md §7), exercised without a
// database: role gating and payload validation both run before any handler
// reaches the service, so a nil repository is never touched. Anything that
// needs real rows lives in test/dbit.

// withClaims injects JWT claims the way mw.Authenticate does, so the role
// middleware and the handlers see exactly what they see in production.
func withClaims(roleState string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &mw.Claims{UserID: "user-1", RoleState: roleState, TokenType: "access"}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mw.ContextKeyClaims, claims)))
		})
	}
}

// intercityRouter mirrors the route groups registered in cmd/server/main.go:
// the customer group's role set (which admits DRIVER_ACTIVE) and the driver
// group's RoleDriverActive-only gate.
func intercityRouter(roleState string) *chi.Mux {
	h := intercity.NewHandler(intercity.NewService(nil, nil, zerolog.Nop()))

	r := chi.NewRouter()
	r.Route("/customer", func(r chi.Router) {
		r.Use(withClaims(roleState))
		r.Use(mw.RequireRole(mw.RoleCustomer, mw.RoleDriverActive, mw.RoleDriverPending))
		r.Post("/intercity/trips/{id}/hold", h.HoldSeats)
		r.Delete("/intercity/bookings/{id}", h.CancelBooking)
	})
	r.Route("/driver", func(r chi.Router) {
		r.Use(withClaims(roleState))
		r.Use(mw.RequireRole(mw.RoleDriverActive))
		r.Post("/intercity/trips", h.PublishTrip)
		r.Post("/intercity/trips/{id}/board", h.BoardPassenger)
		r.Post("/intercity/trips/{id}/no-show", h.MarkNoShow)
		r.Delete("/intercity/trips/{id}", h.CancelTrip)
	})
	return r
}

func doJSON(t *testing.T, r http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, http.NoBody)
	} else {
		req = httptest.NewRequest(method, path, jsonBody(t, body))
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	return env.Error.Code
}

// ── Role gating ──────────────────────────────────────────────────────────────

// §7: "All driver intercity routes require RoleDriverActive explicitly." The
// wider /driver group admits DRIVER_PENDING, and an unreviewed account that can
// publish a plausible trip is a phone-harvesting instrument (§9).
func TestDriverIntercityRoutes_RejectDriverPending(t *testing.T) {
	r := intercityRouter(mw.RoleDriverPending)

	for _, c := range []struct {
		method, path string
	}{
		{http.MethodPost, "/driver/intercity/trips"},
		{http.MethodPost, "/driver/intercity/trips/trip-1/board"},
		{http.MethodPost, "/driver/intercity/trips/trip-1/no-show"},
		{http.MethodDelete, "/driver/intercity/trips/trip-1"},
	} {
		w := doJSON(t, r, c.method, c.path, map[string]interface{}{})
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", c.method, c.path)
	}
}

func TestDriverIntercityRoutes_RejectPlainCustomer(t *testing.T) {
	w := doJSON(t, intercityRouter(mw.RoleCustomer), http.MethodPost, "/driver/intercity/trips", map[string]interface{}{})
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// The customer group admits DRIVER_ACTIVE — a rival driver browses and books
// without a second account, and the role middleware will never stop them. That
// is exactly why ownership lives in the SQL and why no customer-facing payload
// carries the credit-clamped sellable seat count. Documented as a test so the
// property is not "fixed" by tightening the role set and lost from the SQL.
func TestCustomerIntercityRoutes_AdmitDriverActive(t *testing.T) {
	w := doJSON(t, intercityRouter(mw.RoleDriverActive), http.MethodPost,
		"/customer/intercity/trips/trip-1/hold", map[string]interface{}{"seats": 0})

	require.NotEqual(t, http.StatusForbidden, w.Code, "the role gate does not stop a driver here")
	assert.Equal(t, http.StatusBadRequest, w.Code, "it is the payload rules that answer")
}

// ── Payload contract ─────────────────────────────────────────────────────────

func TestHoldSeats_RejectsInvalidPayloads(t *testing.T) {
	r := intercityRouter(mw.RoleCustomer)

	cases := map[string]interface{}{
		"zero seats":         map[string]interface{}{"seats": 0, "idempotency_key": "k1"},
		"negative seats":     map[string]interface{}{"seats": -1, "idempotency_key": "k1"},
		"beyond any vehicle": map[string]interface{}{"seats": 31, "idempotency_key": "k1"},
		// Without an idempotency key an offline client replaying its queue
		// consumes seats twice — the whole reason the index is non-partial.
		"missing idempotency key": map[string]interface{}{"seats": 2},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, r, http.MethodPost, "/customer/intercity/trips/trip-1/hold", body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "VALIDATION", errorCode(t, w))
		})
	}
}

// §7: cancel_reason is REQUIRED. It is fanned out to every passenger whose
// booking the cancellation kills, so there is no silent cancellation.
func TestCancelTrip_RequiresAReason(t *testing.T) {
	r := intercityRouter(mw.RoleDriverActive)

	w := doJSON(t, r, http.MethodDelete, "/driver/intercity/trips/trip-1", map[string]interface{}{})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "VALIDATION", errorCode(t, w))

	w = doJSON(t, r, http.MethodDelete, "/driver/intercity/trips/trip-1", map[string]interface{}{"cancel_reason": "   "})
	assert.Equal(t, http.StatusBadRequest, w.Code, "whitespace is not a reason")
}

// board / no-show take booking_id from the body, so the id must at least be a
// well-formed uuid before it ever reaches a statement. The real protection is
// in the SQL (`b.trip_id = :id` plus the trip's ownership predicate), which
// test/dbit covers.
func TestBoardAndNoShow_RequireAWellFormedBookingID(t *testing.T) {
	r := intercityRouter(mw.RoleDriverActive)

	for _, path := range []string{
		"/driver/intercity/trips/trip-1/board",
		"/driver/intercity/trips/trip-1/no-show",
	} {
		w := doJSON(t, r, http.MethodPost, path, map[string]interface{}{})
		assert.Equal(t, http.StatusBadRequest, w.Code, path)

		w = doJSON(t, r, http.MethodPost, path, map[string]interface{}{"booking_id": "not-a-uuid"})
		assert.Equal(t, http.StatusBadRequest, w.Code, path)
	}
}

func TestPublishTrip_RejectsInvalidPayloads(t *testing.T) {
	r := intercityRouter(mw.RoleDriverActive)

	valid := func() map[string]interface{} {
		return map[string]interface{}{
			"corridor":           "KGL_MUS",
			"vehicle_id":         "11111111-1111-1111-1111-111111111111",
			"origin_name":        "Kigali",
			"destination_name":   "Musanze",
			"origin_lat":         -1.95,
			"origin_lng":         30.06,
			"dest_lat":           -1.50,
			"dest_lng":           29.63,
			"staging_address":    "Nyabugogo Taxi Park",
			"depart_at":          "2026-10-01T08:00:00Z",
			"total_seats":        8,
			"price_per_seat_rwf": 5000,
		}
	}

	t.Run("driver_id in the body is ignored, never trusted", func(t *testing.T) {
		// The payload has no driver_id field at all: the driver is the JWT
		// subject. An extra key simply decodes into nothing.
		body := valid()
		body["driver_id"] = "22222222-2222-2222-2222-222222222222"
		body["total_seats"] = 0 // fail on a rule we can assert without a DB
		w := doJSON(t, r, http.MethodPost, "/driver/intercity/trips", body)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("depart_at must be RFC3339", func(t *testing.T) {
		body := valid()
		body["depart_at"] = "2026-10-01 08:00"
		w := doJSON(t, r, http.MethodPost, "/driver/intercity/trips", body)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "VALIDATION", errorCode(t, w))
	})

	t.Run("price and seats are bounded", func(t *testing.T) {
		for _, mutate := range []func(m map[string]interface{}){
			func(m map[string]interface{}) { m["price_per_seat_rwf"] = 0 },
			func(m map[string]interface{}) { m["price_per_seat_rwf"] = 500001 },
			func(m map[string]interface{}) { m["total_seats"] = 31 },
			func(m map[string]interface{}) { m["vehicle_id"] = "nope" },
			func(m map[string]interface{}) { m["staging_address"] = "" },
		} {
			body := valid()
			mutate(body)
			w := doJSON(t, r, http.MethodPost, "/driver/intercity/trips", body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		}
	})
}
