package tickets

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

// GET /api/v1/admin/tickets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	f := ListFilter{
		Status:   r.URL.Query().Get("status"),
		Priority: r.URL.Query().Get("priority"),
		Type:     r.URL.Query().Get("type"),
		Search:   r.URL.Query().Get("search"),
		Limit:    parseIntDefault(r.URL.Query().Get("limit"), 20),
		Offset:   parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	tickets, total, err := h.svc.List(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"tickets": tickets, "total": total})
}

// GET /api/v1/admin/tickets/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, t)
}

// POST /api/v1/admin/tickets
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Subject    string  `json:"subject"`
		Type       string  `json:"type"`
		Priority   string  `json:"priority"`
		FromRole   string  `json:"from_role"`
		FromUserID *string `json:"from_user_id"`
		RideID     *string `json:"ride_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Subject == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "subject is required")
		return
	}
	if body.Priority == "" {
		body.Priority = "MEDIUM"
	}
	if body.Type == "" {
		body.Type = "OTHER"
	}
	t, err := h.svc.Create(r.Context(), body.Subject, body.Type, body.Priority, body.FromRole, body.FromUserID, body.RideID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, t)
}

// POST /api/v1/admin/tickets/:id/reply
func (h *Handler) Reply(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Author string `json:"author"`
		Body   string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "body is required")
		return
	}
	if body.Author == "" {
		body.Author = "Support Agent"
	}
	if err := h.svc.Reply(r.Context(), id, body.Author, body.Body); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/tickets/:id/assign
func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		AdminID string `json:"admin_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AdminID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "admin_id is required")
		return
	}
	if err := h.svc.Assign(r.Context(), id, body.AdminID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/tickets/:id/resolve
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Resolve(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// PATCH /api/v1/admin/support/tickets/:id  — partial update (status, priority, etc.)
func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Status   string `json:"status"`
		Priority string `json:"priority"`
		AdminID  string `json:"admin_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	if body.Status == "RESOLVED" {
		if err := h.svc.Resolve(r.Context(), id); err != nil {
			respond.Error(w, err)
			return
		}
	} else if body.AdminID != "" {
		if err := h.svc.Assign(r.Context(), id, body.AdminID); err != nil {
			respond.Error(w, err)
			return
		}
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
