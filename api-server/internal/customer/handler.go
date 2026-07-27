package customer

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes customer profile endpoints.
type Handler struct {
	svc *Service
	// Session-bootstrap sources, injected from main to avoid import cycles.
	activeRide func(ctx context.Context, customerID string) (any, error)
	unread     func(ctx context.Context, userID string) (int, error)
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetSessionSources wires the active-ride and unread-count providers used by the
// GET /customer/session bootstrap. Both are optional; nil sources are skipped.
func (h *Handler) SetSessionSources(
	activeRide func(ctx context.Context, customerID string) (any, error),
	unread func(ctx context.Context, userID string) (int, error),
) {
	h.activeRide = activeRide
	h.unread = unread
}

// GET /api/v1/customer/session
// One-call bootstrap for the customer app: profile + active ride + unread count,
// mirroring the driver /session endpoint so the client restores state in one hit.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	profile, err := h.svc.GetProfile(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	session := map[string]any{"profile": profile}

	if h.activeRide != nil {
		if ride, aErr := h.activeRide(r.Context(), claims.UserID); aErr == nil {
			session["active_ride"] = ride // nil when none
		} else {
			session["active_ride"] = nil
		}
	}
	if h.unread != nil {
		if n, uErr := h.unread(r.Context(), claims.UserID); uErr == nil {
			session["unread_notifications"] = n
		}
	}

	respond.OK(w, session)
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
		FullName              *string `json:"full_name"`
		Email                 *string `json:"email"`
		FCMToken              *string `json:"fcm_token"`
		ProfileImageURL       *string `json:"profile_image_url"`
		EmergencyContactName  *string `json:"emergency_contact_name"`
		EmergencyContactPhone *string `json:"emergency_contact_phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	if err := h.svc.UpdateProfile(r.Context(), claims.UserID, ProfileUpdate{
		FullName:              body.FullName,
		Email:                 body.Email,
		FCMToken:              body.FCMToken,
		ProfileImageURL:       body.ProfileImageURL,
		EmergencyContactName:  body.EmergencyContactName,
		EmergencyContactPhone: body.EmergencyContactPhone,
	}); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}
