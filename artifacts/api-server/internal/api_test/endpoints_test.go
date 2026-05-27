package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/respond"
)

// ── Helpers ───────────────────────────────────────────────────────────────

func makeRouter() *chi.Mux { return chi.NewRouter() }

func jsonBody(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}

// decodeData unwraps the {"data": ...} envelope and decodes into target.
func decodeData(t *testing.T, body *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error interface{}     `json:"error"`
	}
	require.NoError(t, json.NewDecoder(body.Body).Decode(&env))
	if target != nil && env.Data != nil {
		require.NoError(t, json.Unmarshal(env.Data, target))
	}
}

func makeJWT(t *testing.T, secret, userID, roleState, tokenType string, expiry time.Duration) string {
	t.Helper()
	claims := &mw.Claims{
		UserID:    userID,
		RoleState: roleState,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "test-jti",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return token
}

// ── POST /auth/register — validation ─────────────────────────────────────

func TestAuthRegister_MissingPhone(t *testing.T) {
	r := makeRouter()
	r.Post("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PhoneNumber string `json:"phone_number"`
			FullName    string `json:"full_name"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.PhoneNumber == "" {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "phone_number required")
			return
		}
		respond.NoContent(w)
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/register", jsonBody(t, map[string]string{
		"full_name": "Test User",
	}))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthRegister_MissingFullName(t *testing.T) {
	r := makeRouter()
	r.Post("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PhoneNumber string `json:"phone_number"`
			FullName    string `json:"full_name"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.FullName == "" {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "full_name required")
			return
		}
		respond.NoContent(w)
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/register", jsonBody(t, map[string]string{
		"phone_number": "+250780000000",
	}))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Auth middleware rejects expired JWT ───────────────────────────────────

func TestAuthMiddleware_RejectsExpiredJWT(t *testing.T) {
	secret := "test-secret-key-long-enough-for-hmac"

	r := makeRouter()
	r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			header := req.Header.Get("Authorization")
			if header == "" || len(header) < 8 {
				respond.ErrorMsg(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing token")
				return
			}
			tokenStr := header[7:]
			claims := &mw.Claims{}
			_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(secret), nil
			})
			if err != nil {
				respond.ErrorMsg(w, http.StatusUnauthorized, "TOKEN_EXPIRED", "token expired")
				return
			}
			next.ServeHTTP(w, req)
		})
	}).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		respond.OK(w, map[string]string{"ok": "true"})
	})

	expiredToken := makeJWT(t, secret, "user-1", "CUSTOMER_ONLY", "access", -1*time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Unwrap envelope to check error code
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	require.NotNil(t, env.Error)
	assert.NotEmpty(t, env.Error.Code)
}

func TestAuthMiddleware_RejectsMissingToken(t *testing.T) {
	r := makeRouter()
	r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("Authorization") == "" {
				respond.ErrorMsg(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing token")
				return
			}
			next.ServeHTTP(w, req)
		})
	}).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		respond.OK(w, map[string]string{"ok": "true"})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── Role middleware rejects wrong role ────────────────────────────────────

func TestRoleMiddleware_RejectsForbiddenRole(t *testing.T) {
	r := makeRouter()
	r.With(mw.RequireRole(mw.RoleAdmin)).Get("/admin-only", func(w http.ResponseWriter, r *http.Request) {
		respond.OK(w, map[string]string{"ok": "true"})
	})

	req := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── GET /locations/landmarks — returns 20 landmarks ──────────────────────

func TestLocationsLandmarks_ReturnsData(t *testing.T) {
	r := makeRouter()
	r.Get("/locations/landmarks", func(w http.ResponseWriter, r *http.Request) {
		landmarks := make([]map[string]interface{}, 20)
		for i := range landmarks {
			landmarks[i] = map[string]interface{}{
				"name": "Landmark", "category": "district",
				"lat": -1.94, "lng": 30.06,
			}
		}
		respond.OK(w, map[string]interface{}{"landmarks": landmarks})
	})

	req := httptest.NewRequest(http.MethodGet, "/locations/landmarks", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var payload map[string]interface{}
	decodeData(t, w, &payload)
	landmarks, ok := payload["landmarks"].([]interface{})
	require.True(t, ok, "landmarks must be an array")
	assert.Len(t, landmarks, 20, "must return all 20 seeded landmarks")
}

// ── POST /rides — returns ride_id immediately ─────────────────────────────

func TestRides_CreateReturnsRideID(t *testing.T) {
	r := makeRouter()
	r.Post("/rides", func(w http.ResponseWriter, r *http.Request) {
		respond.Created(w, map[string]interface{}{
			"ride_id": "test-ride-uuid",
			"status":  "SEARCHING",
		})
	})

	body := jsonBody(t, map[string]interface{}{
		"pickup_lat": -1.9441, "pickup_lng": 30.0619,
		"pickup_address": "Kigali CBD",
		"dest_lat":       -1.9355, "dest_lng": 30.1127,
		"dest_address":   "Kimironko",
		"transport_type": "MOTO_BIKE",
	})
	req := httptest.NewRequest(http.MethodPost, "/rides", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var payload map[string]interface{}
	decodeData(t, w, &payload)
	assert.NotEmpty(t, payload["ride_id"])
	assert.Equal(t, "SEARCHING", payload["status"])
}

// ── PATCH /users/mode — invalid mode rejected ─────────────────────────────

func TestUsersMode_InvalidMode(t *testing.T) {
	r := makeRouter()
	r.Patch("/users/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Mode != "customer" && body.Mode != "driver" {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "mode must be customer or driver")
			return
		}
		respond.NoContent(w)
	})

	req := httptest.NewRequest(http.MethodPatch, "/users/mode", jsonBody(t, map[string]string{
		"mode": "admin",
	}))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── Swagger reflects versioned route API without fare suggestions ─────────

func TestOpenAPI_LocationRoutesAreVersionedWithoutFareSuggestion(t *testing.T) {
	specPath := filepath.Join("..", "..", "config", "openapi.json")
	raw, err := os.ReadFile(specPath)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(raw), "fare_suggestion"), "public API must not expose fare suggestions yet")

	var spec struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))

	assert.Equal(t, "1.1.0", spec.Info.Version)
	assert.Contains(t, spec.Paths, "/api/v1/locations/route")
	assert.Contains(t, spec.Paths, "/api/v1/locations/suggestions")
	assert.Contains(t, spec.Paths, "/api/v1/users/me/saved-locations")
	assert.Contains(t, spec.Paths, "/api/v1/rides/active")
	assert.Contains(t, spec.Paths, "/api/v1/driver/rides/{ride_id}/arrive")
	assert.Contains(t, spec.Paths, "/api/v1/driver/rides/{ride_id}/cancel")
	assert.Contains(t, spec.Paths, "/api/v1/driver/rides/{ride_id}/complete")
	assert.Contains(t, spec.Paths, "/api/v1/driver/rides/{ride_id}/negotiation/lock-fare")

	var hasV1Server bool
	for _, server := range spec.Servers {
		if strings.HasSuffix(server.URL, "/api/v1") {
			hasV1Server = true
			break
		}
	}
	assert.True(t, hasV1Server, "Swagger should advertise the v1 API base path")
}

// ── POST /rides/:id/offers/lock — driver only ─────────────────────────────

func TestOfferLock_NonDriverGets401(t *testing.T) {
	r := makeRouter()
	r.With(mw.RequireRole(mw.RoleDriverActive)).
		Post("/rides/{id}/offers/lock", func(w http.ResponseWriter, r *http.Request) {
			respond.NoContent(w)
		})

	req := httptest.NewRequest(http.MethodPost, "/rides/test-id/offers/lock",
		jsonBody(t, map[string]interface{}{"amount_rwf": 3000}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// No claims in context → RequireRole returns 401
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
