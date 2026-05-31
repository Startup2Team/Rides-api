package ride

import (
	"encoding/json"
	"net/http"
	"strconv"

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

// POST /api/v1/customer/rides
func (h *Handler) CreateRide(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		PickupLat     float64  `json:"pickup_lat"    validate:"required,min=-90,max=90"`
		PickupLng     float64  `json:"pickup_lng"    validate:"required,min=-180,max=180"`
		PickupAddr    string   `json:"pickup_address" validate:"required"`
		DestLat       float64  `json:"dest_lat"      validate:"required,min=-90,max=90"`
		DestLng       float64  `json:"dest_lng"      validate:"required,min=-180,max=180"`
		DestAddr      string   `json:"dest_address"  validate:"required"`
		TransportType string   `json:"transport_type" validate:"required,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
		InitialFare   *float64 `json:"initial_fare"`
		DistanceKM    *float64 `json:"distance_km"`
	}

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

	ride, err := h.svc.CreateRide(r.Context(), claims.UserID, body.TransportType, body.PickupAddr, body.DestAddr, pickup, dest, body.InitialFare, body.DistanceKM)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.Created(w, map[string]interface{}{
		"ride_id": ride.ID,
		"status":  ride.Status,
	})
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
