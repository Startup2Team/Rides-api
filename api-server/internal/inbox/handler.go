package inbox

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

// POST /api/v1/contact  — public, no auth required.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Category string `json:"category"`
		Subject  string `json:"subject"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if body.Name == "" || body.Email == "" || body.Subject == "" || body.Message == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "name, email, subject, and message are required")
		return
	}
	if err := h.svc.Submit(r.Context(), body.Name, body.Email, body.Phone, body.Category, body.Subject, body.Message); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "Message received"})
}

// GET /api/v1/admin/inbox/stats
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.Stats(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/inbox
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	f := ListFilter{
		Status:   r.URL.Query().Get("status"),
		Category: r.URL.Query().Get("category"),
		Search:   r.URL.Query().Get("search"),
		Limit:    parseIntDefault(r.URL.Query().Get("limit"), 20),
		Offset:   parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	msgs, total, err := h.svc.List(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"messages": msgs, "total": total})
}

// GET /api/v1/admin/inbox/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, m)
}

// POST /api/v1/admin/inbox/:id/reply
func (h *Handler) Reply(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "body is required")
		return
	}
	if err := h.svc.Reply(r.Context(), id, body.Body); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/inbox/:id/archive
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Archive(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/inbox/:id/spam
func (h *Handler) Spam(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.MarkSpam(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// PATCH /api/v1/admin/inbox/:id
func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "status is required")
		return
	}
	if err := h.svc.UpdateStatus(r.Context(), id, body.Status); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/admin/inbox/:id
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Delete(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "deleted"})
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
