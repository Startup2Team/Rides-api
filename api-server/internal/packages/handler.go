package packages

import (
	"encoding/json"
	"net/http"

	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes package and credit HTTP endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/driver/packages?vehicle_type=MOTO_BIKE
func (h *Handler) ListPackages(w http.ResponseWriter, r *http.Request) {
	vehicleType := r.URL.Query().Get("vehicle_type")
	if vehicleType == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "vehicle_type query parameter is required")
		return
	}

	pkgs, err := h.svc.ListPackages(r.Context(), vehicleType)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, pkgs)
}

// POST /api/v1/driver/packages/purchase
// Body: { "package_id": "<uuid>" }
func (h *Handler) PurchasePackage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	var body struct {
		PackageID string `json:"package_id" validate:"required,uuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	credit, err := h.svc.BuyPackage(r.Context(), claims.UserID, body.PackageID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.Created(w, credit)
}

// GET /api/v1/driver/credits
func (h *Handler) GetCredits(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	credit, err := h.svc.GetCredits(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{"credit": credit})
}
