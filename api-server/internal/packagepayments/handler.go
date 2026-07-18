package packagepayments

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

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

// GET /api/v1/package-payments/configuration
func (h *Handler) Configuration(w http.ResponseWriter, r *http.Request) {
	if _, ok := userID(r); !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	respond.OK(w, h.svc.Configuration())
}

// GET /api/v1/package-payments/manual-claims
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	claims, err := h.svc.List(r.Context(), uid, limit)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": claims, "next_cursor": nil})
}

// GET /api/v1/package-payments/manual-claims/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	c, err := h.svc.Get(r.Context(), uid, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, c)
}

// POST /api/v1/package-payments/manual-claims
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	c, err := h.svc.Create(r.Context(), uid, in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, map[string]interface{}{"claim": c})
}

// POST /api/v1/package-payments/manual-claims/{id}/submit
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, h.svc.Submit)
}

// POST /api/v1/package-payments/manual-claims/{id}/resubmit
func (h *Handler) Resubmit(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, h.svc.Resubmit)
}

// action runs a submit/resubmit-style transition and returns { claim }.
func (h *Handler) action(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, uid, id string) (*Claim, error)) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in ActionInput
	_ = json.NewDecoder(r.Body).Decode(&in) // body optional
	c, err := fn(r.Context(), uid, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"claim": c})
}

// POST /api/v1/package-payments/manual-claims/{id}/cancel
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in ActionInput
	_ = json.NewDecoder(r.Body).Decode(&in) // body optional
	c, err := h.svc.Cancel(r.Context(), uid, chi.URLParam(r, "id"), in.ReasonCode)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"claim": c})
}
