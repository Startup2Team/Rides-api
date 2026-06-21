package packages

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
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
	svc      *Service
	bonus    BonusAfterPurchase // optional; nil = bonus disabled
	ledger   *LedgerService     // v4 entitlement ledger
	purchase *PurchaseService   // v4 purchase + MoMo lifecycle
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// SetBonus wires the bonus service so purchases automatically trigger bonus grants.
func (h *Handler) SetBonus(b BonusAfterPurchase) { h.bonus = b }

// SetLedger wires the v4 entitlement ledger.
func (h *Handler) SetLedger(l *LedgerService) { h.ledger = l }

// SetPurchase wires the v4 purchase/MoMo service.
func (h *Handler) SetPurchase(p *PurchaseService) { h.purchase = p }

// GET /api/v1/driver/entitlements — v4 vehicle-type balances from the ledger.
func (h *Handler) GetEntitlements(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	ents, err := h.ledger.ListEntitlementsForUser(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"entitlements": ents})
}

// ── Driver endpoints ──────────────────────────────────────────────────────────

// GET /api/v1/driver/packages?vehicle_type=MOTO_BIKE
// Returns the v4 catalog: each package's active version with any active campaign
// override applied (mobile-shaped fields).
func (h *Handler) ListPackages(w http.ResponseWriter, r *http.Request) {
	vehicleType := r.URL.Query().Get("vehicle_type")
	if vehicleType == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "vehicle_type query parameter is required")
		return
	}
	pkgs, err := h.svc.ListCatalog(r.Context(), vehicleType)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if pkgs == nil {
		pkgs = []*CatalogPackage{}
	}
	respond.OK(w, pkgs)
}

// GET /api/v1/driver/campaigns/active?vehicle_type=MOTO_BIKE
func (h *Handler) ListActiveCampaigns(w http.ResponseWriter, r *http.Request) {
	vehicleType := r.URL.Query().Get("vehicle_type")
	campaigns, err := h.svc.ListActiveCampaigns(r.Context(), vehicleType)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if campaigns == nil {
		campaigns = []*Campaign{}
	}
	respond.OK(w, campaigns)
}

// POST /api/v1/driver/packages/purchase
// v4: resolves the active version + campaign, snapshots the offer, and either
// grants immediately (free/promotional) or opens a PENDING MoMo charge. The
// driver polls GET .../purchases/{id} for the final status.
// Body: { "package_id", "idempotency_key", "momo_phone", "momo_provider" }
func (h *Handler) PurchasePackage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var body CreateInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}
	p, err := h.purchase.Create(r.Context(), claims.UserID, body)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, p)
}

// GET /api/v1/driver/packages/purchases/{purchaseID} — status poll.
func (h *Handler) GetPurchaseStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	p, err := h.purchase.GetStatus(r.Context(), claims.UserID, chi.URLParam(r, "purchaseID"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, p)
}

// GET /api/v1/driver/packages/history — the driver's purchase history.
func (h *Handler) PurchaseHistory(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	list, err := h.purchase.History(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"purchases": list})
}

// POST /api/v1/webhooks/momo/callback — MoMo payment notification (public).
// Body: { "payment_ref", "provider_txn_id", "status" } where status is
// SUCCESS|SUCCESSFUL|PAID for success, anything else = failure.
func (h *Handler) WebhookMoMo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PaymentRef    string `json:"payment_ref"`
		ProviderTxnID string `json:"provider_txn_id"`
		Status        string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PaymentRef == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	success := false
	switch body.Status {
	case "SUCCESS", "SUCCESSFUL", "PAID", "COMPLETED":
		success = true
	}
	if err := h.purchase.Confirm(r.Context(), body.PaymentRef, body.ProviderTxnID, success); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "ok"})
}

// GET /api/v1/driver/credits
// v4: total_remaining is summed from the entitlement ledger so the existing
// mobile (which reads total_remaining) stays correct after cutover. The
// per-vehicle breakdown lives at GET /driver/entitlements.
func (h *Handler) GetCredits(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	ents, err := h.ledger.ListEntitlementsForUser(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	total := 0
	for _, e := range ents {
		total += e.TotalRemaining
	}
	respond.OK(w, map[string]interface{}{"total_remaining": total, "entitlements": ents})
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
