package incidents

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/incidents
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	f := ListFilter{
		Status:   r.URL.Query().Get("status"),
		Severity: r.URL.Query().Get("severity"),
		Type:     r.URL.Query().Get("type"),
		Search:   r.URL.Query().Get("search"),
		Limit:    parseIntDefault(r.URL.Query().Get("limit"), 20),
		Offset:   parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	incidents, total, err := h.svc.List(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"incidents": incidents, "total": total})
}

// GET /api/v1/admin/incidents/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inc, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, inc)
}

// POST /api/v1/admin/incidents
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type           string  `json:"type"`
		Severity       string  `json:"severity"`
		Description    string  `json:"description"`
		ReporterRole   string  `json:"reporter_role"`
		LocationText   string  `json:"location_text"`
		District       string  `json:"district"`
		RideID         *string `json:"ride_id"`
		ReporterUserID *string `json:"reporter_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Type == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "type is required")
		return
	}
	if body.Severity == "" {
		body.Severity = "MEDIUM"
	}
	inc, err := h.svc.Create(r.Context(), body.Type, body.Severity, body.Description,
		body.ReporterRole, body.LocationText, body.District, body.RideID, body.ReporterUserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, inc)
}

// POST /api/v1/admin/incidents/:id/acknowledge
func (h *Handler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Acknowledge(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/incidents/:id/escalate
func (h *Handler) Escalate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Escalate(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/incidents/:id/resolve
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Notes string `json:"notes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.svc.Resolve(r.Context(), id, body.Notes); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/incidents/:id/message
func (h *Handler) Message(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "message is required")
		return
	}
	if err := h.svc.AddMessage(r.Context(), id, body.Message); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
