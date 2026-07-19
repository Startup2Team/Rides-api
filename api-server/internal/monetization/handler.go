package monetization

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/audit"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc   *Service
	audit *audit.Logger
}

func NewHandler(svc *Service, auditLog *audit.Logger) *Handler {
	return &Handler{svc: svc, audit: auditLog}
}

func adminCtx(r *http.Request) (id, role string) {
	claims := mw.GetClaims(r)
	if claims == nil {
		return "", ""
	}
	return claims.UserID, claims.AdminRole
}

// ── Partners ──────────────────────────────────────────────────────────────

func (h *Handler) ListPartners(w http.ResponseWriter, r *http.Request) {
	partners, err := h.svc.ListPartners(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, partners)
}

func (h *Handler) GetPartner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	partner, err := h.svc.GetPartnerByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, partner)
}

func (h *Handler) CreatePartner(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	var input CreatePartnerInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid payload")
		return
	}

	p, err := h.svc.CreatePartner(r.Context(), input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "partner.create", "partners", p.ID, "Created advertising partner: "+p.Name, map[string]any{"partner": p})
	respond.Created(w, p)
}

func (h *Handler) UpdatePartner(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	id := chi.URLParam(r, "id")
	var input UpdatePartnerInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid payload")
		return
	}

	p, err := h.svc.UpdatePartner(r.Context(), id, input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "partner.update", "partners", id, "Updated advertising partner: "+p.Name, map[string]any{"updates": input})
	respond.OK(w, p)
}

func (h *Handler) DeletePartner(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	id := chi.URLParam(r, "id")

	err := h.svc.DeletePartner(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "partner.delete", "partners", id, "Deleted advertising partner: "+id, map[string]any{"partner_id": id})
	respond.OK(w, map[string]string{"status": "success"})
}

// ── Adverts ───────────────────────────────────────────────────────────────

func (h *Handler) ListAdverts(w http.ResponseWriter, r *http.Request) {
	adverts, err := h.svc.ListAdverts(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, adverts)
}

func (h *Handler) GetAdvert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	advert, err := h.svc.GetAdvertByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, advert)
}

func (h *Handler) CreateAdvert(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	var input CreateAdvertInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid payload")
		return
	}

	a, err := h.svc.CreateAdvert(r.Context(), input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "advert.create", "adverts", a.ID, "Created banner advert: "+a.Headline, map[string]any{"advert": a})
	respond.Created(w, a)
}

func (h *Handler) UpdateAdvert(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	id := chi.URLParam(r, "id")
	var input UpdateAdvertInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid payload")
		return
	}

	a, err := h.svc.UpdateAdvert(r.Context(), id, input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "advert.update", "adverts", id, "Updated banner advert: "+a.Headline, map[string]any{"updates": input})
	respond.OK(w, a)
}

func (h *Handler) DeleteAdvert(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	id := chi.URLParam(r, "id")

	err := h.svc.DeleteAdvert(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "advert.delete", "adverts", id, "Deleted banner advert: "+id, map[string]any{"advert_id": id})
	respond.OK(w, map[string]string{"status": "success"})
}

// ── Mobile API (Public) ───────────────────────────────────────────────────

func (h *Handler) ListActiveAdverts(w http.ResponseWriter, r *http.Request) {
	adverts, err := h.svc.ListActiveAdverts(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, adverts)
}
