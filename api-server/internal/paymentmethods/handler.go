package paymentmethods

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func userID(r *http.Request) (string, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.UserID == "" {
		return "", false
	}
	return claims.UserID, true
}

// GET /api/v1/payments/methods
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	items, err := h.svc.List(r.Context(), uid)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": items})
}

// GET /api/v1/payments/methods/default
func (h *Handler) Default(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	m, err := h.svc.Default(r.Context(), uid)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, m) // nil serialises as null
}

// GET /api/v1/payments/billing-profile
func (h *Handler) BillingProfile(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	bp, err := h.svc.BillingProfile(r.Context(), uid)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, bp)
}

// POST /api/v1/payments/methods
func (h *Handler) Add(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in AddInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	items, err := h.svc.Add(r.Context(), uid, in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, map[string]interface{}{"items": items})
}

// PATCH /api/v1/payments/methods/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	items, err := h.svc.Update(r.Context(), uid, chi.URLParam(r, "id"), in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": items})
}

// PATCH /api/v1/payments/methods/{id}/default
func (h *Handler) SetDefault(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	items, err := h.svc.SetDefault(r.Context(), uid, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": items})
}

// DELETE /api/v1/payments/methods/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	items, err := h.svc.Delete(r.Context(), uid, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": items})
}
