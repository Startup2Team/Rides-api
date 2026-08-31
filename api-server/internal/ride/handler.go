package ride

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes ride HTTP endpoints for customers.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// createRideRequest is the payload for CreateRide. Named (rather than inline)
// so its validator tags can be unit-tested directly against
// go-playground/validator without spinning up an HTTP request — see
// TestCreateRideRequest_Validation.
type createRideRequest struct {
	// No `required` on the coordinates: go-playground/validator treats it as
	// "reject the zero value", which would reject lat==0 (the equator, which
	// runs through Uganda) or lng==0 (prime meridian). min/max bounds plus
	// the service-level geo.Point.Validate() are the real range checks.
	PickupLat      float64  `json:"pickup_lat"    validate:"min=-90,max=90"`
	PickupLng      float64  `json:"pickup_lng"    validate:"min=-180,max=180"`
	PickupAddr     string   `json:"pickup_address" validate:"required"`
	DestLat        float64  `json:"dest_lat"      validate:"min=-90,max=90"`
	DestLng        float64  `json:"dest_lng"      validate:"min=-180,max=180"`
	DestAddr       string   `json:"dest_address"  validate:"required"`
	TransportType  string   `json:"transport_type" validate:"required,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
	InitialFare    *float64 `json:"initial_fare"`
	DistanceKM     *float64 `json:"distance_km"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// POST /api/v1/customer/rides
func (h *Handler) CreateRide(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body createRideRequest

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	pickup := geo.Point{Lat: body.PickupLat, Lng: body.PickupLng}
	dest := geo.Point{Lat: body.DestLat, Lng: body.DestLng}

	ride, routeInfo, err := h.svc.CreateRide(r.Context(), claims.UserID, body.TransportType, body.PickupAddr, body.DestAddr, pickup, dest, body.InitialFare, body.DistanceKM, body.IdempotencyKey)
	if err != nil {
		respond.Error(w, err)
		return
	}

	// Expose the search budget so the app can show an honest countdown instead
	// of hardcoding its own guess. Mirrors the matching engine's fallback for a
	// non-positive config value. The deadline is anchored on the ride's DB
	// creation time, which is when StartSearch fires.
	giveUpSeconds := h.svc.cfg.Matching.GiveUpSeconds
	if giveUpSeconds <= 0 {
		giveUpSeconds = 90
	}

	resp := map[string]interface{}{
		"ride_id":            ride.ID,
		"status":             ride.Status,
		"ride_version":       ride.RideVersion,
		"give_up_seconds":    giveUpSeconds,
		"search_deadline_at": ride.CreatedAt.Add(time.Duration(giveUpSeconds) * time.Second).UTC().Format(time.RFC3339),
	}
	// Additive: present ONLY when OSRM returned a real road route for this
	// pickup→destination pair; absent entirely on OSRM-off, timeout, NoRoute,
	// or any Haversine-fallback result (routeInfo is nil in all those cases —
	// see realRouteInfo in service.go). Informational only — draw-the-path /
	// precise-ETA fields, never used to derive the fare.
	if routeInfo != nil {
		resp["route_distance_km"] = routeInfo.DistanceKM
		resp["route_duration_minutes"] = routeInfo.DurationMinutes
		if routeInfo.DurationSeconds != nil {
			resp["route_duration_seconds"] = *routeInfo.DurationSeconds
		}
		if routeInfo.Geometry != nil {
			resp["route_geometry"] = *routeInfo.Geometry
		}
	}

	respond.Created(w, resp)
}

// GET /api/v1/customer/rides/:ride_id
func (h *Handler) GetRide(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	ride, err := h.svc.GetRide(r.Context(), rideID, claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, ride.ToResponse())
}

// GET /api/v1/customer/rides
func (h *Handler) ListRides(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	rides, err := h.svc.repo.ListByCustomer(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}

	responses := make([]*RideResponse, len(rides))
	for i, ride := range rides {
		responses[i] = ride.ToResponse()
	}

	respond.OK(w, map[string]interface{}{
		"rides":  responses,
		"limit":  limit,
		"offset": offset,
	})
}

// GET /api/v1/driver/rides — the signed-in driver's ride history (paginated).
func (h *Handler) ListDriverRides(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	rides, err := h.svc.repo.ListByDriver(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}

	responses := make([]*RideResponse, len(rides))
	for i, ride := range rides {
		responses[i] = ride.ToResponse()
	}

	respond.OK(w, map[string]interface{}{
		"rides":  responses,
		"limit":  limit,
		"offset": offset,
	})
}

// DELETE /api/v1/customer/rides/:ride_id
func (h *Handler) CancelRide(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reason == "" {
		body.Reason = "customer cancelled"
	}

	if err := h.svc.CancelRide(r.Context(), rideID, claims.UserID, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// GET /api/v1/driver/rides/:ride_id
func (h *Handler) GetRideForDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	ride, err := h.svc.GetRideForDriver(r.Context(), rideID, claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride.ToResponse())
}

// GET /api/v1/driver/rides/active
func (h *Handler) GetActiveRideForDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	ride, err := h.svc.GetActiveRideForDriver(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride.ToResponse())
}

// updateCustomerLocationRequest is the payload for UpdateCustomerLocation.
// Named (rather than inline) so its validator tags can be unit-tested
// directly against go-playground/validator without spinning up an HTTP
// request — see TestUpdateCustomerLocationRequest_Validation.
type updateCustomerLocationRequest struct {
	// No `required`: go-playground/validator treats it as "reject the zero
	// value", which would reject lat==0 (the equator, which runs through
	// Uganda) or lng==0. min/max bounds plus the service-level
	// geo.Point.Validate() are the real range checks.
	Lat float64 `json:"lat" validate:"min=-90,max=90"`
	Lng float64 `json:"lng" validate:"min=-180,max=180"`
	// Cached but not currently fanned out to the driver (WS payload is
	// lat/lng only). Bounded anyway so a malformed/malicious client can't
	// stuff nonsense into Redis: 300 km/h is well above any road vehicle,
	// 0-360 is the full compass.
	SpeedKMH *float64 `json:"speed_kmh" validate:"omitempty,gte=0,lte=300"`
	Heading  *float64 `json:"heading"   validate:"omitempty,gte=0,lte=360"`
}

// sanitizeOptionalTelemetry nils out an out-of-range heading/speed so a bad
// OPTIONAL field can't fail validation and reject the whole update. Phones
// report coords.heading/speed as -1 — a number, not null — when the
// course/speed is unknown (stationary, coarse GPS); forwarding that -1 would
// otherwise 400 the request and silently drop the rider's live lat/lng. Lat/Lng
// are still validated by the caller after this runs.
func (b *updateCustomerLocationRequest) sanitizeOptionalTelemetry() {
	if b.Heading != nil && (*b.Heading < 0 || *b.Heading > 360) {
		b.Heading = nil
	}
	if b.SpeedKMH != nil && (*b.SpeedKMH < 0 || *b.SpeedKMH > 300) {
		b.SpeedKMH = nil
	}
}

// POST /api/v1/rides/{id}/customer-location
// Customer publishes their live GPS position, relayed to the assigned driver.
// Whole-trip sharing: only accepted while the ride is in an active status
// (CustomerLocationShareStatuses) and only for the ride's own customer.
func (h *Handler) UpdateCustomerLocation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "id")

	var body updateCustomerLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	// Drop out-of-range optional telemetry (e.g. the -1 "unknown" heading/speed
	// phones report) BEFORE validation, so a bad optional field never 400s the
	// whole update and discards the essential lat/lng. See method doc.
	body.sanitizeOptionalTelemetry()
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	update := CustomerLocationUpdate{
		Lat:      body.Lat,
		Lng:      body.Lng,
		SpeedKMH: body.SpeedKMH,
		Heading:  body.Heading,
	}

	if err := h.svc.UpdateCustomerLocation(r.Context(), rideID, claims.UserID, update); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// GET /api/v1/customer/rides/active
// Returns the customer's current non-terminal ride for app-restart recovery.
// 404 when the customer has no active ride.
func (h *Handler) GetActiveRideForCustomer(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	ride, err := h.svc.GetActiveRide(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride.ToResponse())
}
