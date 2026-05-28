package settings

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/settings
func (h *Handler) GetAll(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.GetAll(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// PUT /api/v1/admin/settings/commission
func (h *Handler) UpdateCommission(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r, "commission")
}

// PUT /api/v1/admin/settings/negotiation
func (h *Handler) UpdateNegotiation(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r, "negotiation")
}

// PUT /api/v1/admin/settings/fares
func (h *Handler) UpdateFares(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r, "fares")
}

// PUT /api/v1/admin/settings/integrations
func (h *Handler) UpdateIntegrations(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r, "integrations")
}

// PUT /api/v1/admin/settings/notifications
func (h *Handler) UpdateNotifications(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r, "notifications")
}

// PUT /api/v1/admin/settings/regions/:id
func (h *Handler) UpdateRegion(w http.ResponseWriter, r *http.Request) {
	regionID := chi.URLParam(r, "id")
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil || len(updates) == 0 {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "body must be a non-empty JSON object")
		return
	}
	if err := h.svc.UpdateRegion(r.Context(), regionID, updates); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request, key string) {
	var value interface{}
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	if err := h.svc.Update(r.Context(), key, value); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}
