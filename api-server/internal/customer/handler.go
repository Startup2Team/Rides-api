package customer

import (
	"encoding/json"
	"net/http"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes customer profile endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/customer/profile
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	profile, err := h.svc.GetProfile(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, profile)
}

// GET /api/v1/customer/level
// Returns the customer's loyalty level (gamification) + progress to the next tier.
func (h *Handler) GetLevel(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	level, err := h.svc.GetLevel(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, level)
}

// PUT /api/v1/customer/profile
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		FullName        *string `json:"full_name"`
		Email           *string `json:"email"`
		FCMToken        *string `json:"fcm_token"`
		ProfileImageURL *string `json:"profile_image_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	if err := h.svc.UpdateProfile(r.Context(), claims.UserID, body.FullName, body.Email, body.FCMToken, body.ProfileImageURL); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}
