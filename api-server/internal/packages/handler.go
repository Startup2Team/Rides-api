package packages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/audit"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// BonusAfterPurchase is the subset of bonus.Service called after a package purchase.
type BonusAfterPurchase interface {
	AfterPackagePurchase(ctx context.Context, driverID, creditID, vehicleTypeID string, expiresAt time.Time) (any, error)
}

// Handler exposes package and credit HTTP endpoints.
type Handler struct {
	svc   *Service
	audit *audit.Logger
	bonus BonusAfterPurchase // optional; nil = bonus disabled
}

func NewHandler(svc *Service, auditLog *audit.Logger) *Handler {
	return &Handler{svc: svc, audit: auditLog}
}

// SetBonus wires the bonus service so purchases automatically trigger bonus grants.
func (h *Handler) SetBonus(b BonusAfterPurchase) { h.bonus = b }

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

	// Trigger purchase bonus asynchronously — never blocks or fails the purchase.
	var bonusGrant interface{}
	if h.bonus != nil {
		bonusGrant, _ = h.bonus.AfterPackagePurchase(
			r.Context(), claims.UserID, credit.ID, credit.VehicleTypeID, credit.ExpiresAt,
		)
	}

	respond.Created(w, map[string]interface{}{
		"credit": credit,
		"bonus":  bonusGrant, // nil if no bonus applied this purchase
	})
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
	total, _ := h.svc.GetTotalCredits(r.Context(), claims.UserID)
	respond.OK(w, map[string]interface{}{"credit": credit, "total_remaining": total})
}

// ── Admin endpoints ───────────────────────────────────────────────────────────

// adminCtx pulls the admin id + role off the request claims for audit entries.
func adminCtx(r *http.Request) (id, role string) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return "", ""
	}
	return claims.UserID, claims.AdminRole
}

// GET /api/v1/admin/packages
func (h *Handler) AdminListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := h.svc.AdminListPackages(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, pkgs)
}

// POST /api/v1/admin/packages
func (h *Handler) AdminCreatePackage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name" validate:"required"`
		VehicleTypeCode string `json:"vehicle_type_code" validate:"required"`
		RideCount       int    `json:"ride_count" validate:"required,min=1"`
		BonusRides      int    `json:"bonus_rides" validate:"min=0"`
		ValidityDays    int    `json:"validity_days" validate:"required,min=1"`
		PriceRWF        int    `json:"price_rwf" validate:"min=0"`
		IsPromotional   bool   `json:"is_promotional"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	pkg, err := h.svc.AdminCreatePackage(r.Context(), body.Name, body.VehicleTypeCode, body.RideCount, body.BonusRides, body.ValidityDays, body.PriceRWF, body.IsPromotional)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "package.create", "ride_packages", pkg.ID, fmt.Sprintf("Created package %s (price: %d RWF)", pkg.Name, pkg.PriceRWF), map[string]any{"package": pkg})

	respond.Created(w, pkg)
}

// PATCH /api/v1/admin/packages/{id}
func (h *Handler) AdminUpdatePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name         *string `json:"name"`
		RideCount    *int    `json:"ride_count"`
		BonusRides   *int    `json:"bonus_rides"`
		ValidityDays *int    `json:"validity_days"`
		PriceRWF     *int    `json:"price_rwf"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	pkg, err := h.svc.AdminUpdatePackage(r.Context(), id, body.Name, body.RideCount, body.BonusRides, body.ValidityDays, body.PriceRWF)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "package.update", "ride_packages", pkg.ID, fmt.Sprintf("Updated package %s", pkg.Name), map[string]any{"updates": body})

	respond.OK(w, pkg)
}

// POST /api/v1/admin/packages/{id}/toggle
func (h *Handler) AdminTogglePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	err := h.svc.AdminTogglePackage(r.Context(), id, body.IsActive)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	action := "package.deactivate"
	if body.IsActive {
		action = "package.activate"
	}
	h.audit.Record(r.Context(), adminID, role, action, "ride_packages", id, fmt.Sprintf("Toggled package active status to %t", body.IsActive), map[string]any{"is_active": body.IsActive})

	respond.OK(w, map[string]string{"status": "success"})
}

// DELETE /api/v1/admin/packages/{id}
func (h *Handler) AdminDeletePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "id path parameter is required")
		return
	}

	// Fetch name first for audit logging
	pkg, err := h.svc.GetPackageByID(r.Context(), id)
	var pkgName string
	if err == nil && pkg != nil {
		pkgName = pkg.Name
	} else {
		pkgName = id
	}

	err = h.svc.AdminDeletePackage(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "package.delete", "ride_packages", id, fmt.Sprintf("Soft-deleted package %s", pkgName), map[string]any{"package_id": id})

	respond.OK(w, map[string]string{"status": "success"})
}
