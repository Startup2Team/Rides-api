package analytics

import (
	"net/http"
	"strconv"

	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes read-only admin analytics endpoints.
type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// GET /api/v1/admin/analytics/overview
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.Overview(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/rides/daily
func (h *Handler) DailyRides(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	data, err := h.repo.DailyRides(r.Context(), days)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/rides/weekly
func (h *Handler) WeeklyRides(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.WeeklyRides(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/revenue/breakdown
func (h *Handler) RevenueBreakdown(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.RevenueBreakdown(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/drivers/performance
func (h *Handler) DriverPerformance(w http.ResponseWriter, r *http.Request) {
	limit := 50
	data, err := h.repo.DriverPerformance(r.Context(), limit)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/negotiation/stats
func (h *Handler) NegotiationStats(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.NegotiationStats(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/heatmap
func (h *Handler) Heatmap(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.Heatmap(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/analytics/cancellations
func (h *Handler) Cancellations(w http.ResponseWriter, r *http.Request) {
	data, err := h.repo.CancellationStats(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}
