package notification

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
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

// PATCH /api/v1/users/me/notifications/{id}/unread
func (h *Handler) MarkUnread(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	if err := h.repo.MarkUnread(r.Context(), id, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/users/me/notifications/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id, claims.UserID); err != nil {
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

// POST /api/v1/users/me/device-token  { "token": "...", "platform": "android" }
// Registers (or refreshes) an FCM token for the caller so pushes reach every
// device they're signed in on.
func (h *Handler) RegisterDeviceToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "token is required")
		return
	}
	if err := h.repo.UpsertDeviceToken(r.Context(), claims.UserID, body.Token, body.Platform); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/users/me/device-token  { "token": "..." }
// Unregisters a token (e.g. logout on that device).
func (h *Handler) UnregisterDeviceToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.repo.DeleteDeviceToken(r.Context(), claims.UserID, body.Token); err != nil {
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
