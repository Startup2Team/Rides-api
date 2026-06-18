package notification

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

// GET /api/v1/users/me/notifications?limit=20&offset=0
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	limit := parseIntOr(r.URL.Query().Get("limit"), 30)
	offset := parseIntOr(r.URL.Query().Get("offset"), 0)

	notifs, err := h.repo.ListByUser(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	unread, _ := h.repo.UnreadCount(r.Context(), claims.UserID)

	respond.OK(w, map[string]interface{}{
		"notifications": notifs,
		"unread_count":  unread,
	})
}

// GET /api/v1/users/me/notifications/unread-count
func (h *Handler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	count, err := h.repo.UnreadCount(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]int{"unread_count": count})
}

// PATCH /api/v1/users/me/notifications/{id}/read
func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	if err := h.repo.MarkRead(r.Context(), id, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/users/me/notifications/mark-all-read
func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if err := h.repo.MarkAllRead(r.Context(), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

func parseIntOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
