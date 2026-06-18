package bonus

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes bonus endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// ── Driver endpoints ──────────────────────────────────────────────────────────

// GET /api/v1/driver/bonuses
// Returns the caller's bonus grant history.
func (h *Handler) DriverGrants(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	grants, err := h.svc.DriverGrants(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"grants": grants})
}

// GET /api/v1/driver/bonuses/tiers
// Returns active bonus tiers so the mobile can show "buy and earn X rides free".
func (h *Handler) ListActiveTiers(w http.ResponseWriter, r *http.Request) {
	tiers, err := h.svc.ListTiers(r.Context(), true)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"tiers": tiers})
}

// ── Admin endpoints ───────────────────────────────────────────────────────────

// GET /api/v1/admin/bonuses/tiers
func (h *Handler) AdminListTiers(w http.ResponseWriter, r *http.Request) {
	tiers, err := h.svc.ListTiers(r.Context(), false)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"tiers": tiers})
}

// POST /api/v1/admin/bonuses/tiers
// Body: { "name", "description", "trigger_type", "purchase_number", "bonus_rides", "vehicle_type_id" }
func (h *Handler) AdminCreateTier(w http.ResponseWriter, r *http.Request) {
	var in CreateTierInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	tier, err := h.svc.CreateTier(r.Context(), &in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, tier)
}

// DELETE /api/v1/admin/bonuses/tiers/{tierID}
// Soft-deactivates a tier — drivers stop earning it on future purchases.
func (h *Handler) AdminDeactivateTier(w http.ResponseWriter, r *http.Request) {
	tierID := chi.URLParam(r, "tierID")
	if err := h.svc.DeactivateTier(r.Context(), tierID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "deactivated"})
}

// PUT /api/v1/admin/bonuses/tiers/{tierID}/activate
func (h *Handler) AdminActivateTier(w http.ResponseWriter, r *http.Request) {
	tierID := chi.URLParam(r, "tierID")
	if err := h.svc.ActivateTier(r.Context(), tierID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "activated"})
}
