package dashboard

import (
	"net/http"
	"strconv"
	"time"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

const maxRangeDays = 365

// GET /api/v1/admin/dashboard
//
// Query params (period-aware fields only):
//   - from, to  Exact range (YYYY-MM-DD). Both must be provided together.
//                Window is half-open [from 00:00, to+1 00:00) so the "to" day
//                is included.
//   - days      Last N days (1..365). Default 1. Ignored if from/to are set.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q.Get("from"), q.Get("to"), q.Get("days"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	snap, err := h.svc.Get(r.Context(), win)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, snap)
}

// GET /api/v1/admin/dashboard/revenue-series
//
// Same query params as /admin/dashboard. Returns two parallel time series
// (current window + same-length previous window) plus peak and totals.
func (h *Handler) RevenueSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q.Get("from"), q.Get("to"), q.Get("days"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	out, err := h.svc.RevenueSeries(r.Context(), win)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/rides-series — bucketed completed/cancelled rides
// over the requested window plus same-length previous window for comparison.
func (h *Handler) RidesSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q.Get("from"), q.Get("to"), q.Get("days"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	out, err := h.svc.RidesSeries(r.Context(), win)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/driver-status — current online / on-trip / offline counts.
// No period — this is always "right now".
func (h *Handler) DriverStatusSnapshot(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.DriverStatusSnapshot(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/top-drivers — drivers with most completed rides
// in the selected window. Accepts the same period params as /admin/dashboard,
// plus optional `limit` (default 10, max 50).
func (h *Handler) TopDrivers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q.Get("from"), q.Get("to"), q.Get("days"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	limit := 10
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	out, err := h.svc.TopDrivers(r.Context(), win, limit)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/live-map — driver positions, active-ride count,
// online-driver count, and pickup heatmap for the last 2 hours.
func (h *Handler) LiveMap(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.LiveMap(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/recent-activity?limit=N&beforeId=ID&type=ride.created
// Default limit 10, max 100. beforeId enables cursor pagination (descending by id).
func (h *Handler) RecentActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := RecentActivityFilter{Limit: 10, Type: q.Get("type")}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			f.Limit = n
		}
	}
	if b := q.Get("beforeId"); b != "" {
		if n, err := strconv.ParseInt(b, 10, 64); err == nil && n > 0 {
			f.BeforeID = n
		}
	}
	out, err := h.svc.RecentActivity(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

// GET /api/v1/admin/dashboard/alerts?limit=N&kind=incident|ticket
// Default limit 10, max 100.
func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := AlertFilter{Limit: 10, Kind: q.Get("kind")}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			f.Limit = n
		}
	}
	if f.Kind != "" && f.Kind != "incident" && f.Kind != "ticket" {
		respond.Error(w, badRequest("'kind' must be 'incident' or 'ticket'"))
		return
	}
	out, err := h.svc.Alerts(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, out)
}

func badRequest(msg string) error {
	return apperrors.New(http.StatusBadRequest, "BAD_REQUEST", msg)
}

func parseWindow(fromStr, toStr, daysStr string) (Window, error) {
	// Custom range path
	if fromStr != "" || toStr != "" {
		if fromStr == "" || toStr == "" {
			return Window{}, badRequest("both 'from' and 'to' are required for a custom range")
		}
		from, err := time.Parse("2006-01-02", fromStr)
		if err != nil {
			return Window{}, badRequest("'from' must be YYYY-MM-DD")
		}
		to, err := time.Parse("2006-01-02", toStr)
		if err != nil {
			return Window{}, badRequest("'to' must be YYYY-MM-DD")
		}
		if from.After(to) {
			return Window{}, badRequest("'from' must be on or before 'to'")
		}
		if from.After(time.Now().UTC()) {
			return Window{}, badRequest("'from' cannot be in the future")
		}
		// Make "to" inclusive: shift to next-day midnight so the full day counts.
		toExclusive := to.AddDate(0, 0, 1)
		if toExclusive.Sub(from).Hours()/24 > maxRangeDays {
			return Window{}, badRequest("range cannot exceed 365 days")
		}
		return Window{From: from, To: toExclusive}, nil
	}

	// Last-N-days path
	days := 1
	if daysStr != "" {
		n, err := strconv.Atoi(daysStr)
		if err != nil || n < 1 || n > maxRangeDays {
			return Window{}, badRequest("'days' must be an integer between 1 and 365")
		}
		days = n
	}
	return Window{Days: days}, nil
}
