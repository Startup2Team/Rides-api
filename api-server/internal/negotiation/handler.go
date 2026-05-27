package negotiation

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes negotiation endpoints for both customers and drivers.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// POST /api/v1/customer/rides/:ride_id/negotiation/propose
// POST /api/v1/driver/rides/:ride_id/negotiation/propose
func (h *Handler) Propose(role string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := middleware.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")

		var body struct {
			Amount float64 `json:"amount" validate:"required,gt=0"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respond.Error(w, apperrors.ErrBadRequest)
			return
		}
		if err := validate.Struct(body); err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}

		if err := h.svc.Propose(r.Context(), rideID, role, claims.UserID, body.Amount); err != nil {
			respond.Error(w, err)
			return
		}

		respond.NoContent(w)
	}
}

// POST /api/v1/customer/rides/:ride_id/negotiation/accept
// POST /api/v1/driver/rides/:ride_id/negotiation/accept
func (h *Handler) Accept(role string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := middleware.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")

		if err := h.svc.Accept(r.Context(), rideID, role, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}

		respond.NoContent(w)
	}
}

// POST /api/v1/customer/rides/:ride_id/negotiation/decline
// POST /api/v1/driver/rides/:ride_id/negotiation/decline
func (h *Handler) Decline(role string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := middleware.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")

		if err := h.svc.Decline(r.Context(), rideID, role, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}

		respond.NoContent(w)
	}
}

// POST /api/v1/driver/rides/:ride_id/negotiation/lock-fare
func (h *Handler) LockManualFare(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	var body struct {
		Amount float64 `json:"amount" validate:"required,gt=0"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.LockManualFare(r.Context(), rideID, claims.UserID, body.Amount); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// POST /api/v1/driver/rides/:ride_id/negotiation/initiate-call
func (h *Handler) InitiateCall(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	maskedNumber, err := h.svc.InitiateCall(r.Context(), rideID, claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]string{"masked_number": maskedNumber})
}
