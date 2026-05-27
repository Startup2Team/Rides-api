package location

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/internal/ride"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes location, landmark, saved location, suggestion, and mode endpoints.
type Handler struct {
	svc     *Service
	rideSvc *ride.Service
}

func NewHandler(svc *Service, rideSvc *ride.Service) *Handler {
	return &Handler{svc: svc, rideSvc: rideSvc}
}

// GET /api/v1/locations/landmarks
func (h *Handler) GetLandmarks(w http.ResponseWriter, r *http.Request) {
	landmarks, err := h.svc.GetLandmarks(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"landmarks": landmarks})
}

// GET /api/v1/locations/suggestions
func (h *Handler) GetSuggestions(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	result, err := h.svc.GetSuggestions(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, result)
}

// POST /api/v1/locations/route
// Body: pickup_lat, pickup_lng, dest_lat, dest_lng, vehicle_type, distance_km, duration_minutes
func (h *Handler) UpsertRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PickupLat       float64 `json:"pickup_lat"       validate:"required"`
		PickupLng       float64 `json:"pickup_lng"       validate:"required"`
		DestLat         float64 `json:"dest_lat"         validate:"required"`
		DestLng         float64 `json:"dest_lng"         validate:"required"`
		VehicleType     string  `json:"vehicle_type"     validate:"required,oneof=MOTO_BIKE CAB_TAXI LIGHT_HILUX HEAVY_FUSO"`
		DistanceKM      float64 `json:"distance_km"      validate:"required,gt=0"`
		DurationMinutes int     `json:"duration_minutes" validate:"required,gt=0"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	result, err := h.svc.UpsertRoute(r.Context(),
		body.PickupLat, body.PickupLng, body.DestLat, body.DestLng,
		body.VehicleType, body.DistanceKM, body.DurationMinutes,
	)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{
		"route": result,
	})
}

// GET /api/v1/locations/route?pickup_lat=&pickup_lng=&dest_lat=&dest_lng=&vehicle_type=
func (h *Handler) GetRoute(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var pickupLat, pickupLng, destLat, destLng float64
	vehicleType := q.Get("vehicle_type")

	if _, err := fmt.Sscanf(q.Get("pickup_lat"), "%f", &pickupLat); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if _, err := fmt.Sscanf(q.Get("pickup_lng"), "%f", &pickupLng); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if _, err := fmt.Sscanf(q.Get("dest_lat"), "%f", &destLat); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if _, err := fmt.Sscanf(q.Get("dest_lng"), "%f", &destLng); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	result, err := h.svc.GetRoute(r.Context(), pickupLat, pickupLng, destLat, destLng, vehicleType)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{
		"route": result,
	})
}

// ── Saved Locations ───────────────────────────────────────────────────────

// GET /api/v1/users/me/saved-locations
func (h *Handler) ListSavedLocations(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	locs, err := h.svc.ListSavedLocations(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"saved_locations": locs})
}

// POST /api/v1/users/me/saved-locations
func (h *Handler) CreateSavedLocation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		Label   string  `json:"label"   validate:"required"`
		Address string  `json:"address" validate:"required"`
		Lat     float64 `json:"lat"     validate:"required"`
		Lng     float64 `json:"lng"     validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	loc, err := h.svc.CreateSavedLocation(r.Context(), claims.UserID, body.Label, body.Address, body.Lat, body.Lng)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, loc)
}

// PUT /api/v1/users/me/saved-locations/:id
func (h *Handler) UpdateSavedLocation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")

	var body struct {
		Label   string  `json:"label"   validate:"required"`
		Address string  `json:"address" validate:"required"`
		Lat     float64 `json:"lat"     validate:"required"`
		Lng     float64 `json:"lng"     validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.UpdateSavedLocation(r.Context(), id, claims.UserID, body.Label, body.Address, body.Lat, body.Lng); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/users/me/saved-locations/:id
func (h *Handler) DeleteSavedLocation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")

	if err := h.svc.DeleteSavedLocation(r.Context(), id, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Mode Switching ────────────────────────────────────────────────────────

// PATCH /api/v1/users/mode
func (h *Handler) SwitchMode(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		Mode string `json:"mode" validate:"required,oneof=customer driver"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.SwitchMode(r.Context(), claims.UserID, body.Mode); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Active Ride ───────────────────────────────────────────────────────────

// GET /api/v1/rides/active — reconnect recovery
func (h *Handler) GetActiveRide(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	activeRide, err := h.rideSvc.GetActiveRide(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, apperrors.ErrNotFound)
		return
	}
	respond.OK(w, activeRide)
}
