package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes admin HTTP endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/drivers
func (h *Handler) ListDrivers(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, offset := paginate(r)

	drivers, err := h.svc.ListDrivers(r.Context(), status, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"drivers": drivers, "limit": limit, "offset": offset})
}

// POST /api/v1/admin/drivers/:id/approve
func (h *Handler) ApproveDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")

	if err := h.svc.ApproveDriver(r.Context(), profileID, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/reject
func (h *Handler) RejectDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")

	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.svc.RejectDriver(r.Context(), profileID, claims.UserID, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/suspend
func (h *Handler) SuspendDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")

	var body struct {
		Reason        string `json:"reason"`
		DurationHours int    `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DurationHours <= 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	if err := h.svc.SuspendDriver(r.Context(), profileID, claims.UserID, body.Reason, body.DurationHours); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// GET /api/v1/admin/users
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginate(r)
	users, err := h.svc.ListUsers(r.Context(), limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"users": users, "limit": limit, "offset": offset})
}

// POST /api/v1/admin/users/:id/suspend
func (h *Handler) SuspendUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	var body struct {
		DurationHours int `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DurationHours <= 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	if err := h.svc.SuspendUser(r.Context(), userID, body.DurationHours); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// GET /api/v1/admin/flags/gps-anomalies
func (h *Handler) GPSAnomalies(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.GPSAnomalies(r.Context(), 200)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/flags/device-collisions
func (h *Handler) DeviceCollisions(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.DeviceCollisions(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/rides
func (h *Handler) ListRides(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginate(r)
	rides, err := h.svc.ListRides(r.Context(), limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"rides": rides, "limit": limit, "offset": offset})
}

func paginate(r *http.Request) (int, int) {
	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, _ := strconv.Atoi(l); n > 0 && n <= 100 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, _ := strconv.Atoi(o); n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
