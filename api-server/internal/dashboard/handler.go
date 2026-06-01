package dashboard

import (
	"net/http"

	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/dashboard
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	snap, err := h.svc.Get(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, snap)
}
