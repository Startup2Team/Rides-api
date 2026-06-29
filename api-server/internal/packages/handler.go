package packages

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/config"
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
	svc      *Service
	audit    *audit.Logger
	cfg      *config.Config
	bonus    BonusAfterPurchase // optional; nil = bonus disabled
	ledger   *LedgerService     // v4 entitlement ledger
	purchase *PurchaseService   // v4 purchase + MoMo lifecycle
}

func NewHandler(svc *Service, auditLog *audit.Logger, cfg *config.Config) *Handler {
	return &Handler{svc: svc, audit: auditLog, cfg: cfg}
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

// GET /api/v1/driver/packages[?vehicle_type=MOTO_BIKE]
// Returns the v4 catalog: each package's active version with any active campaign
// override applied (mobile-shaped fields). vehicle_type is optional — omitting it
// returns the full catalog across all vehicle types in one response, so the app
// fetches once instead of once per vehicle type.
func (h *Handler) ListPackages(w http.ResponseWriter, r *http.Request) {
	vehicleType := r.URL.Query().Get("vehicle_type")
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
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // cap at 1 MB

	// 1. IP Whitelisting (if configured)
	clientIP := middleware.TrustedIP(r)
	if h.cfg != nil && h.cfg.MoMo.IPWhitelist != "" {
		allowedIPs := strings.Split(h.cfg.MoMo.IPWhitelist, ",")
		allowed := false
		for _, ip := range allowedIPs {
			if strings.TrimSpace(ip) == clientIP {
				allowed = true
				break
			}
		}
		if !allowed {
			respond.ErrorMsg(w, http.StatusForbidden, "IP_NOT_ALLOWED", "Unauthorized IP address")
			return
		}
	}

	// 2. Cryptographic signature verification (HMAC-SHA256)
	if h.cfg != nil && h.cfg.MoMo.WebhookSecret != "" {
		signature := r.Header.Get("X-Signature")
		if signature == "" {
			respond.ErrorMsg(w, http.StatusUnauthorized, "MISSING_SIGNATURE", "X-Signature header required")
			return
		}

		mac := hmac.New(sha256.New, []byte(h.cfg.MoMo.WebhookSecret))
		mac.Write(raw)
		expectedMAC := mac.Sum(nil)
		expectedSig := hex.EncodeToString(expectedMAC)

		if subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) != 1 {
			respond.ErrorMsg(w, http.StatusUnauthorized, "INVALID_SIGNATURE", "Invalid cryptographic signature")
			return
		}
	}

	var body struct {
		PaymentRef    string `json:"payment_ref"`
		ProviderTxnID string `json:"provider_txn_id"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.PaymentRef == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	success := false
	switch body.Status {
	case "SUCCESS", "SUCCESSFUL", "PAID", "COMPLETED":
		success = true
	}
	if err := h.purchase.Confirm(r.Context(), body.PaymentRef, body.ProviderTxnID, success, raw); err != nil {
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
	var input CreatePackageInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	pkg, err := h.svc.AdminCreatePackage(r.Context(), &input)
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
	var input UpdatePackageInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	pkg, err := h.svc.AdminUpdatePackage(r.Context(), id, &input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "package.update", "ride_packages", pkg.ID, fmt.Sprintf("Updated package %s", pkg.Name), map[string]any{"updates": input})

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

// GET /api/v1/admin/campaigns
func (h *Handler) AdminListCampaigns(w http.ResponseWriter, r *http.Request) {
	campaigns, err := h.svc.AdminListCampaigns(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, campaigns)
}

// POST /api/v1/admin/campaigns
func (h *Handler) AdminCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var input CreateCampaignInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	adminID, role := adminCtx(r)
	campaign, err := h.svc.AdminCreateCampaign(r.Context(), adminID, &input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "campaign.create", "campaigns", campaign.ID, fmt.Sprintf("Created campaign %s (code: %s)", campaign.Name, campaign.Code), map[string]any{"campaign": campaign})

	respond.Created(w, campaign)
}

// PATCH /api/v1/admin/campaigns/{id}
func (h *Handler) AdminUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "id path parameter is required")
		return
	}

	var input UpdateCampaignInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(input); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	campaign, err := h.svc.AdminUpdateCampaign(r.Context(), id, &input)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	action := "campaign.update"
	if input.Status != nil {
		if *input.Status == "ACTIVE" {
			action = "campaign.activate"
		} else if *input.Status == "EXPIRED" {
			action = "campaign.expire"
		} else if *input.Status == "ARCHIVED" {
			action = "campaign.archive"
		}
	}
	h.audit.Record(r.Context(), adminID, role, action, "campaigns", campaign.ID, fmt.Sprintf("Updated campaign %s", campaign.Name), map[string]any{"updates": input})

	respond.OK(w, campaign)
}

// DELETE /api/v1/admin/campaigns/{id}
func (h *Handler) AdminDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "id path parameter is required")
		return
	}

	campaign, err := h.svc.GetCampaignByID(r.Context(), id)
	var campaignName string
	if err == nil && campaign != nil {
		campaignName = campaign.Name
	} else {
		campaignName = id
	}

	err = h.svc.AdminDeleteCampaign(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "campaign.delete", "campaigns", id, fmt.Sprintf("Soft-deleted (archived) campaign %s", campaignName), map[string]any{"campaign_id": id})

	respond.OK(w, map[string]string{"status": "success"})
}

// GET /api/v1/admin/packages/purchases
func (h *Handler) AdminListPurchases(w http.ResponseWriter, r *http.Request) {
	purchases, err := h.purchase.ListAllPurchases(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, purchases)
}

// POST /api/v1/admin/packages/purchases — admin records a purchase on a driver's
// behalf (cash / bank / their own MoMo). Body: { driver_id, package_id, mark_paid }.
// With mark_paid=true (default) credits are granted immediately.
func (h *Handler) AdminCreatePurchase(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	var body struct {
		DriverID  string `json:"driver_id"`
		PackageID string `json:"package_id"`
		MarkPaid  *bool  `json:"mark_paid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	in := AdminCreateInput{DriverID: body.DriverID, PackageID: body.PackageID, MarkPaid: true}
	if body.MarkPaid != nil {
		in.MarkPaid = *body.MarkPaid
	}
	if err := validate.Struct(in); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}
	p, err := h.purchase.AdminCreateOnBehalf(r.Context(), adminID, in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	h.audit.Record(r.Context(), adminID, role, "package_purchase.admin_create", "package_purchases", p.ID,
		fmt.Sprintf("Admin recorded purchase for driver %s (mark_paid=%t)", in.DriverID, in.MarkPaid),
		map[string]any{"driver_id": in.DriverID, "package_id": in.PackageID, "mark_paid": in.MarkPaid, "status": p.Status})
	respond.Created(w, p)
}

// POST /api/v1/admin/packages/purchases/{id}/confirm — admin settles a PENDING
// purchase after verifying payment out-of-band. Body: { success } (default true).
func (h *Handler) AdminConfirmPurchase(w http.ResponseWriter, r *http.Request) {
	adminID, role := adminCtx(r)
	id := chi.URLParam(r, "id")
	var body struct {
		Success *bool `json:"success"`
	}
	// Body is optional — default to success=true.
	_ = json.NewDecoder(r.Body).Decode(&body)
	success := true
	if body.Success != nil {
		success = *body.Success
	}
	p, err := h.purchase.AdminConfirm(r.Context(), id, adminID, success)
	if err != nil {
		respond.Error(w, err)
		return
	}
	h.audit.Record(r.Context(), adminID, role, "package_purchase.admin_confirm", "package_purchases", id,
		fmt.Sprintf("Admin manually settled purchase %s (success=%t) → %s", id, success, p.Status),
		map[string]any{"purchase_id": id, "success": success, "status": p.Status})
	respond.OK(w, p)
}
