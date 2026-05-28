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

// ── Drivers ───────────────────────────────────────────────────────────────

// GET /api/v1/admin/drivers
func (h *Handler) ListDrivers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	drivers, total, err := h.svc.ListDrivers(r.Context(),
		q.Get("status"), q.Get("vehicle_type"), q.Get("search"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"drivers": drivers, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/drivers/overview
func (h *Handler) DriverOverview(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.DriverOverview(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
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

// POST /api/v1/admin/drivers/:id/reinstate
func (h *Handler) ReinstateDriver(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	if err := h.svc.ReinstateDriver(r.Context(), profileID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Customers ─────────────────────────────────────────────────────────────

// GET /api/v1/admin/customers
func (h *Handler) ListCustomers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	customers, total, err := h.svc.ListCustomers(r.Context(),
		q.Get("status"), q.Get("search"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"customers": customers, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/customers/:id
func (h *Handler) GetCustomer(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	customer, err := h.svc.GetCustomer(r.Context(), userID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, customer)
}

// GET /api/v1/admin/users  (kept for backwards compat — delegates to ListCustomers)
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	h.ListCustomers(w, r)
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

// POST /api/v1/admin/customers/:id/reinstate
func (h *Handler) ReinstateUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if err := h.svc.ReinstateUser(r.Context(), userID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Rides ─────────────────────────────────────────────────────────────────

// GET /api/v1/admin/rides
func (h *Handler) ListRides(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	rides, total, err := h.svc.ListRides(r.Context(),
		q.Get("status"), q.Get("transport_type"), q.Get("search"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"rides": rides, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/rides/:id
func (h *Handler) GetRide(w http.ResponseWriter, r *http.Request) {
	rideID := chi.URLParam(r, "id")
	ride, err := h.svc.GetRide(r.Context(), rideID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride)
}

// ── Negotiations ──────────────────────────────────────────────────────────

// GET /api/v1/admin/negotiations
func (h *Handler) ListNegotiations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	negs, total, err := h.svc.ListNegotiations(r.Context(),
		q.Get("status"), q.Get("search"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"negotiations": negs, "total": total, "limit": limit, "offset": offset})
}

// ── Revenue / transactions ────────────────────────────────────────────────

// GET /api/v1/admin/revenue/kpis
func (h *Handler) RevenueKPIs(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "today"
	}
	data, err := h.svc.RevenueKPIs(r.Context(), period)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/revenue/transactions
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	txns, total, err := h.svc.ListTransactions(r.Context(),
		q.Get("status"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"transactions": txns, "total": total, "limit": limit, "offset": offset})
}

// ── Safety flags ──────────────────────────────────────────────────────────

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

// ── Helpers ───────────────────────────────────────────────────────────────

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
