package packages

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

// Handler exposes package and credit HTTP endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// ── Driver endpoints ──────────────────────────────────────────────────────────

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
// Deducts the package price from the driver's wallet, then grants ride credits.
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
	credit, err := h.svc.BuyPackageFromWallet(r.Context(), claims.UserID, body.PackageID)
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

// ── Admin endpoints ───────────────────────────────────────────────────────────

// GET /api/v1/admin/packages
func (h *Handler) AdminListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := h.svc.AdminListPackages(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"packages": pkgs})
}

// POST /api/v1/admin/packages
// Body: { "name": "Moto Starter", "vehicle_type_code": "MOTO_BIKE",
//
//	"ride_count": 20, "validity_days": 30, "price_rwf": 600,
//	"cost_per_ride_rwf": 30, "is_promotional": false }
func (h *Handler) AdminCreatePackage(w http.ResponseWriter, r *http.Request) {
	var input CreatePackageInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	pkg, err := h.svc.AdminCreatePackage(r.Context(), &input)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, pkg)
}

// PATCH /api/v1/admin/packages/{packageID}
// Body: any subset of { "name", "ride_count", "validity_days", "price_rwf",
//
//	"cost_per_ride_rwf", "is_promotional" }
func (h *Handler) AdminUpdatePackage(w http.ResponseWriter, r *http.Request) {
	packageID := chi.URLParam(r, "packageID")
	if packageID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "packageID required")
		return
	}
	var input UpdatePackageInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	pkg, err := h.svc.AdminUpdatePackage(r.Context(), packageID, &input)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, pkg)
}

// DELETE /api/v1/admin/packages/{packageID}
// Soft-deactivates the package (drivers can no longer see or buy it).
func (h *Handler) AdminDeactivatePackage(w http.ResponseWriter, r *http.Request) {
	packageID := chi.URLParam(r, "packageID")
	if err := h.svc.AdminDeactivatePackage(r.Context(), packageID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "deactivated"})
}

// PUT /api/v1/admin/packages/{packageID}/activate
func (h *Handler) AdminActivatePackage(w http.ResponseWriter, r *http.Request) {
	packageID := chi.URLParam(r, "packageID")
	if err := h.svc.AdminActivatePackage(r.Context(), packageID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "activated"})
}
