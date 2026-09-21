//go:build integration

package dbit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/intercity"
	"github.com/workspace/ride-platform/internal/middleware"
)

// The RESPONSE SHAPE of every intercity endpoint, asserted through the real
// handlers against the real database.
//
// This is not decoration. The driver Intercity screen showed "No routes
// available" on healthy staging because the client destructured
// `{"data": [...]}` from an endpoint that answers `{"data": {"corridors": [...]}}`
// — and `.map` on an object threw inside React Query, so the failure surfaced
// as an empty state with no API error anywhere. The shape below is the house
// convention (ride/handler.go ListRides answers `{"data":{"rides":[...],...}}`,
// and every one of the ~40 collection endpoints in internal/ is keyed the same
// way; not one returns a bare array). Pinning it here means the next client
// reads the contract off a passing test instead of off a screen that fails
// silently.
func TestIntercityResponseShapes(t *testing.T) {
	ctx := context.Background()
	corridor := intercityCorridor(t, ctx)

	driverUserID, vehicleID := newIntercityDriverWithVehicle(t, ctx, "COASTER", 18)
	svc := newIntercityService()
	trip, err := svc.PublishTrip(ctx, driverUserID, publishInput(t, ctx, vehicleID, 18))
	require.NoError(t, err)
	retireTrip(t, trip.ID)

	customerID := insertIntercityCustomer(t, ctx)
	r := intercityRouter(svc)

	t.Run("corridors is data.corridors[]", func(t *testing.T) {
		var body struct {
			Data struct {
				Corridors []struct {
					Code            string `json:"code"`
					OriginName      string `json:"origin_name"`
					DestinationName string `json:"destination_name"`
				} `json:"corridors"`
			} `json:"data"`
		}
		doJSON(t, r, customerID, http.MethodGet, "/customer/intercity/corridors", nil, http.StatusOK, &body)
		require.NotEmpty(t, body.Data.Corridors, "the active corridor must be listed")
		require.NotEmpty(t, body.Data.Corridors[0].Code)
		require.NotEmpty(t, body.Data.Corridors[0].OriginName)

		// And it is an OBJECT, not an array: the exact confusion that broke the
		// screen. A client doing `data.map(...)` here throws.
		require.Equal(t, json.Delim('{'), firstToken(t, r, customerID, "/customer/intercity/corridors"))
	})

	t.Run("customer trip search is data.trips[] with limit/offset", func(t *testing.T) {
		var body struct {
			Data struct {
				Trips  []map[string]interface{} `json:"trips"`
				Limit  int                      `json:"limit"`
				Offset int                      `json:"offset"`
			} `json:"data"`
		}
		doJSON(t, r, customerID, http.MethodGet,
			"/customer/intercity/trips?corridor="+corridor+"&seats=1", nil, http.StatusOK, &body)
		require.Equal(t, 20, body.Data.Limit, "house default page size")
		require.Equal(t, 0, body.Data.Offset)
		require.NotEmpty(t, body.Data.Trips)

		// The trip keys a client reads. `seats_available` is the remaining-seat
		// number; there is deliberately no `sellable_seats` (it encodes the
		// operator's credit balance) and no driver phone on the browse path.
		got := body.Data.Trips[0]
		for _, key := range []string{
			"id", "corridor", "origin_name", "destination_name", "staging_address",
			"depart_at", "price_per_seat_rwf", "total_seats", "seats_available",
			"max_seats_per_booking", "status", "driver_first_name", "operator_name",
			"vehicle_type", "vehicle_plate",
		} {
			require.Contains(t, got, key, "trip payload key")
		}
		require.NotContains(t, got, "sellable_seats", "never expose the credit clamp")
		require.NotContains(t, got, "driver_phone", "driver contact is for confirmed passengers only")
	})

	t.Run("trip detail is the trip object directly under data", func(t *testing.T) {
		var body struct {
			Data struct {
				ID             string `json:"id"`
				SeatsAvailable int    `json:"seats_available"`
				TotalSeats     int    `json:"total_seats"`
			} `json:"data"`
		}
		doJSON(t, r, customerID, http.MethodGet, "/customer/intercity/trips/"+trip.ID, nil, http.StatusOK, &body)
		require.Equal(t, trip.ID, body.Data.ID)
		require.Equal(t, 18, body.Data.TotalSeats)
		require.Equal(t, 18, body.Data.SeatsAvailable)
	})

	// A hold, so the booking and manifest shapes are asserted with real rows.
	var bookingID string
	t.Run("hold answers 201 with the booking object under data", func(t *testing.T) {
		var body struct {
			Data struct {
				ID              string `json:"id"`
				TripID          string `json:"trip_id"`
				Seats           int    `json:"seats"`
				Status          string `json:"status"`
				PricePerSeatRWF int    `json:"price_per_seat_rwf"`
				TotalRWF        int    `json:"total_rwf"`
				HoldExpiresAt   string `json:"hold_expires_at"`
			} `json:"data"`
		}
		doJSON(t, r, customerID, http.MethodPost, "/customer/intercity/trips/"+trip.ID+"/hold",
			map[string]interface{}{"seats": 2, "idempotency_key": uniqueKey("shape")},
			http.StatusCreated, &body)
		require.Equal(t, intercity.BookingHeld, body.Data.Status)
		require.Equal(t, 2, body.Data.Seats)
		require.Equal(t, 6000, body.Data.TotalRWF, "integer RWF, price x seats")
		bookingID = body.Data.ID
		require.NotEmpty(t, body.Data.HoldExpiresAt,
			"a HELD booking carries its expiry — the passenger's countdown to confirm")
	})

	t.Run("customer bookings is data.bookings[]", func(t *testing.T) {
		var body struct {
			Data struct {
				Bookings []struct {
					ID     string                 `json:"id"`
					TripID string                 `json:"trip_id"`
					Trip   map[string]interface{} `json:"trip"`
				} `json:"bookings"`
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"data"`
		}
		doJSON(t, r, customerID, http.MethodGet, "/customer/intercity/bookings", nil, http.StatusOK, &body)
		require.Len(t, body.Data.Bookings, 1)
		require.Equal(t, bookingID, body.Data.Bookings[0].ID)
		require.Equal(t, trip.ID, body.Data.Bookings[0].TripID)
		require.NotEmpty(t, body.Data.Bookings[0].Trip, "the list embeds its trip, so the client needs no N+1")
	})

	t.Run("driver trips is data.trips[]", func(t *testing.T) {
		var body struct {
			Data struct {
				Trips []struct {
					ID string `json:"id"`
				} `json:"trips"`
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"data"`
		}
		doJSON(t, r, driverUserID, http.MethodGet, "/driver/intercity/trips", nil, http.StatusOK, &body)
		require.Len(t, body.Data.Trips, 1)
		require.Equal(t, trip.ID, body.Data.Trips[0].ID)
	})

	t.Run("manifest is the manifest object under data", func(t *testing.T) {
		var body struct {
			Data struct {
				TripID        string `json:"trip_id"`
				Status        string `json:"status"`
				TotalSeats    int    `json:"total_seats"`
				HeldSeats     int    `json:"held_seats"`
				BookedSeats   int    `json:"booked_seats"`
				PhonesVisible bool   `json:"phones_visible"`
				Passengers    []struct {
					BookingID string `json:"booking_id"`
					FirstName string `json:"first_name"`
					Seats     int    `json:"seats"`
					Status    string `json:"status"`
					Phone     string `json:"phone"`
				} `json:"passengers"`
			} `json:"data"`
		}
		doJSON(t, r, driverUserID, http.MethodGet, "/driver/intercity/trips/"+trip.ID+"/manifest",
			nil, http.StatusOK, &body)
		require.Equal(t, trip.ID, body.Data.TripID)
		require.Equal(t, 18, body.Data.TotalSeats)
		require.Equal(t, 2, body.Data.HeldSeats)
		require.Len(t, body.Data.Passengers, 1)
		require.Equal(t, bookingID, body.Data.Passengers[0].BookingID)
		require.False(t, body.Data.PhonesVisible,
			"a departure two hours out is outside the unmasking window")
	})
}

// The client must be able to tell BEFORE it opens the Intercity screen whether
// this driver's vehicle can ever publish — a moto driver reaching a screen
// whose only possible outcome is a refusal is a door that leads nowhere. The
// flag rides on the vehicle payload the driver app already fetches (vehicle
// list and driver session), so there is no new endpoint and no extra round trip.
func TestDriverVehiclesCarryIntercityEligibility(t *testing.T) {
	ctx := context.Background()
	svc := driver.NewService(driver.NewRepository(pool), nil, nil, &config.Config{}, zerolog.Nop())

	motoUser, _ := newIntercityDriverWithVehicle(t, ctx, "MOTO_BIKE", 1)
	cabUser, _ := newIntercityDriverWithVehicle(t, ctx, "CAB_TAXI", 4)

	eligible := func(userID string) bool {
		vehicles, err := svc.ListVehicles(ctx, userID)
		require.NoError(t, err)
		require.Len(t, vehicles, 1)
		// Serialised, because the flag only helps if it reaches the wire.
		raw, err := json.Marshal(vehicles[0])
		require.NoError(t, err)
		var onTheWire map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &onTheWire))
		require.Contains(t, onTheWire, "intercity_eligible")
		return onTheWire["intercity_eligible"].(bool)
	}

	require.False(t, eligible(motoUser), "a moto can never publish an intercity trip")
	require.True(t, eligible(cabUser), "a cab is the smallest vehicle that can")
}

// --- plumbing ---------------------------------------------------------------

// intercityRouter mounts the intercity handlers exactly as cmd/server does,
// minus the auth middleware: claims are injected per request below, since this
// suite is about response shape and not about who may call what (ownership is
// covered by the journey and transition tests).
func intercityRouter(svc *intercity.Service) http.Handler {
	h := intercity.NewHandler(svc)
	r := chi.NewRouter()

	r.Route("/customer/intercity", func(r chi.Router) {
		r.Get("/corridors", h.ListCorridors)
		r.Get("/trips", h.ListTrips)
		r.Get("/trips/{id}", h.GetTrip)
		r.Post("/trips/{id}/hold", h.HoldSeats)
		r.Get("/bookings", h.ListBookings)
		r.Get("/bookings/{id}", h.GetBooking)
	})
	r.Route("/driver/intercity", func(r chi.Router) {
		r.Post("/trips", h.PublishTrip)
		r.Get("/trips", h.ListDriverTrips)
		r.Get("/trips/{id}/manifest", h.GetManifest)
	})
	return r
}

func asUser(req *http.Request, userID string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(),
		middleware.ContextKeyClaims, &middleware.Claims{UserID: userID, TokenType: "access"}))
}

func doJSON(t *testing.T, r http.Handler, userID, method, path string, body interface{}, wantStatus int, out interface{}) {
	t.Helper()
	var payload *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		payload = bytes.NewReader(raw)
	} else {
		payload = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, payload)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, asUser(req, userID))

	require.Equal(t, wantStatus, w.Code, "%s %s -> %s", method, path, w.Body.String())
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), out), w.Body.String())
}

// firstToken returns the first JSON token inside the `data` envelope member —
// '{' for an object, '[' for an array.
func firstToken(t *testing.T, r http.Handler, userID, path string) json.Token {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	doJSON(t, r, userID, http.MethodGet, path, nil, http.StatusOK, &envelope)
	tok, err := json.NewDecoder(bytes.NewReader(envelope.Data)).Token()
	require.NoError(t, err)
	return tok
}
