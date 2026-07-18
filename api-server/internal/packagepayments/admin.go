package packagepayments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// PackageGranter grants an approved claim's package rides/credits to the driver.
// Implemented by the packages purchase service (SetGranter in main), which
// snapshots the offer, records a PAID purchase, and posts to the entitlement
// ledger via the SAME grant path as a real MoMo settlement. Returns the
// resulting purchase id so the claim can reference it.
type PackageGranter interface {
	GrantForManualClaim(ctx context.Context, userID, packageID, adminID string) (purchaseID string, err error)
}

// Notifier creates an in-app notification for the driver. Implemented by the
// notification service (PersistForUser). FCM delivery is owned elsewhere — we
// only persist the in-app record here.
type Notifier interface {
	PersistForUser(ctx context.Context, userID, title, body, nType string, data map[string]string)
}

// SetGranter wires the entitlement grant path used on approval.
func (s *Service) SetGranter(g PackageGranter) { s.granter = g }

// SetNotifier wires the in-app notification sink used on approval/rejection.
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// ── Repository (admin, cross-user) ───────────────────────────────────────────

// FindByIDAdmin loads a claim by id without user scoping and also returns the
// owner's auth user id (needed for granting + notifying the driver).
func (r *Repository) FindByIDAdmin(ctx context.Context, id string) (*Claim, string, error) {
	var ownerUserID string
	c := &Claim{}
	row := r.db.QueryRow(ctx, `SELECT user_id, `+claimCols+` FROM manual_payment_claims WHERE id = $1`, id)
	if err := row.Scan(
		&ownerUserID,
		&c.ID, &c.Version, &c.DriverID, &c.VehicleID, &c.VehicleType, &c.OfferID, &c.PackageID,
		&c.PackageVersion, &c.PackageName, &c.ExpectedAmountRwf, &c.Provider,
		&c.MerchantCodeSnapshot, &c.PayerPhoneNumber, &c.TransactionReference,
		&c.ProofImageID, &c.Status, &c.CreatedAt, &c.SubmittedAt, &c.ExpiresAt, &c.ReviewedAt,
		&c.ReviewedBy, &c.RejectionReason, &c.ClarificationMessage, &c.SupportNote,
		&c.ActivationID, &c.PurchaseTransactionID, &c.UpdatedAt, &c.IdempotencyKey,
	); err != nil {
		return nil, "", err
	}
	return c, ownerUserID, nil
}

// ListForReview returns claims across all drivers filtered by status (for the
// admin review queue), newest first.
func (r *Repository) ListForReview(ctx context.Context, status string, limit int) ([]*Claim, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+claimCols+` FROM manual_payment_claims
		 WHERE ($1 = '' OR status = $1) ORDER BY submitted_at DESC NULLS LAST, created_at DESC LIMIT $2`,
		status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]*Claim, 0)
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// ApproveByAdmin flips a claim to approved and records the grant references.
func (r *Repository) ApproveByAdmin(ctx context.Context, id, adminID, purchaseID string) (*Claim, error) {
	return scanClaim(r.db.QueryRow(ctx, `
		UPDATE manual_payment_claims SET
			status                  = 'approved',
			version                 = version + 1,
			reviewed_at             = NOW(),
			reviewed_by             = $2::uuid,
			purchase_transaction_id = $3,
			activation_id           = $3,
			rejection_reason        = NULL,
			clarification_message   = NULL,
			updated_at              = NOW()
		WHERE id = $1
		RETURNING `+claimCols, id, adminID, purchaseID))
}

// RejectByAdmin flips a claim to rejected with a reason and an optional
// clarification message the driver sees before resubmitting.
func (r *Repository) RejectByAdmin(ctx context.Context, id, adminID, reason, clarification string) (*Claim, error) {
	return scanClaim(r.db.QueryRow(ctx, `
		UPDATE manual_payment_claims SET
			status                = 'rejected',
			version               = version + 1,
			reviewed_at           = NOW(),
			reviewed_by           = $2::uuid,
			rejection_reason      = NULLIF($3,''),
			clarification_message = NULLIF($4,''),
			updated_at            = NOW()
		WHERE id = $1
		RETURNING `+claimCols, id, adminID, reason, clarification))
}

// ── Service (admin) ───────────────────────────────────────────────────────────

// ListForReview returns the admin review queue. An empty status returns all.
func (s *Service) ListForReview(ctx context.Context, status string, limit int) ([]*Claim, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	claims, err := s.repo.ListForReview(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	for _, c := range claims {
		if _, err := s.hydrate(ctx, c); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

// Approve verifies a submitted claim, grants the package's rides/credits to the
// driver via the entitlement ledger, records the grant references, and notifies
// the driver. Only a 'submitted' claim can be approved; an already-approved
// claim is returned unchanged (idempotent).
func (s *Service) Approve(ctx context.Context, id, adminID string) (*Claim, error) {
	current, ownerUserID, err := s.repo.FindByIDAdmin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	if current.Status == "approved" {
		return s.hydrate(ctx, current) // idempotent
	}
	if current.Status != "submitted" {
		return nil, errClaimConflict
	}
	if s.granter == nil {
		return nil, apperrors.New(http.StatusServiceUnavailable, "GRANTER_UNAVAILABLE", "package granting is not configured")
	}

	purchaseID, err := s.granter.GrantForManualClaim(ctx, ownerUserID, current.PackageID, adminID)
	if err != nil {
		return nil, err
	}

	updated, err := s.repo.ApproveByAdmin(ctx, id, adminID, purchaseID)
	if err != nil {
		return nil, err
	}
	_ = s.repo.AddAudit(ctx, id, "admin", &adminID, "approved", nil)
	s.notifyDriver(ctx, ownerUserID, updated, "approved", "")
	return s.hydrate(ctx, updated)
}

// Reject declines a submitted claim with a reason (and optional clarification the
// driver reads before resubmitting), then notifies the driver. No credits are
// granted. Only a 'submitted' claim can be rejected.
func (s *Service) Reject(ctx context.Context, id, adminID, reason, clarification string) (*Claim, error) {
	current, ownerUserID, err := s.repo.FindByIDAdmin(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	if current.Status != "submitted" {
		return nil, errClaimConflict
	}

	updated, err := s.repo.RejectByAdmin(ctx, id, adminID, reason, clarification)
	if err != nil {
		return nil, err
	}
	rc := reason
	_ = s.repo.AddAudit(ctx, id, "admin", &adminID, "rejected", &rc)
	s.notifyDriver(ctx, ownerUserID, updated, "rejected", clarification)
	return s.hydrate(ctx, updated)
}

// notifyDriver persists an in-app notification for the driver on a review
// decision. Best-effort; a nil notifier (or persist failure) never blocks the
// review. FCM delivery is handled by the notification service / push agent.
func (s *Service) notifyDriver(ctx context.Context, userID string, c *Claim, decision, clarification string) {
	if s.notifier == nil || userID == "" {
		return
	}
	var title, body string
	data := map[string]string{
		"type":       "package_payment_" + decision,
		"claim_id":   c.ID,
		"package_id": c.PackageID,
	}
	switch decision {
	case "approved":
		title = "Payment approved"
		body = "Your payment for " + c.PackageName + " was approved. Your ride credits are now available."
	case "rejected":
		title = "Payment needs attention"
		if clarification != "" {
			body = clarification
		} else {
			body = "We couldn't verify your payment for " + c.PackageName + ". Please review and resubmit."
		}
	default:
		return
	}
	// NOTE: notifications.type is VARCHAR(20); keep this classifier ≤20 chars.
	// The review outcome (approved/rejected) is carried in data["type"].
	s.notifier.PersistForUser(ctx, userID, title, body, "pkg_payment_review", data)
}

// ── Handler (admin) ───────────────────────────────────────────────────────────

func adminID(r *http.Request) (string, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.UserID == "" {
		return "", false
	}
	return claims.UserID, true
}

// GET /api/v1/admin/package-payments/manual-claims?status=submitted
func (h *Handler) AdminList(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminID(r); !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "submitted" // default review queue
	}
	if strings.EqualFold(status, "all") {
		status = ""
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	claims, err := h.svc.ListForReview(r.Context(), status, limit)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"items": claims, "next_cursor": nil})
}

// POST /api/v1/admin/package-payments/manual-claims/{id}/approve
func (h *Handler) AdminApprove(w http.ResponseWriter, r *http.Request) {
	aid, ok := adminID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	c, err := h.svc.Approve(r.Context(), chi.URLParam(r, "id"), aid)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"claim": c})
}

// AdminRejectInput is the reject body.
type AdminRejectInput struct {
	ReasonCode           string `json:"reason_code"`
	Reason               string `json:"reason"`
	ClarificationMessage string `json:"clarification_message"`
}

// POST /api/v1/admin/package-payments/manual-claims/{id}/reject
func (h *Handler) AdminReject(w http.ResponseWriter, r *http.Request) {
	aid, ok := adminID(r)
	if !ok {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var in AdminRejectInput
	_ = json.NewDecoder(r.Body).Decode(&in) // body optional
	reason := in.Reason
	if reason == "" {
		reason = in.ReasonCode
	}
	c, err := h.svc.Reject(r.Context(), chi.URLParam(r, "id"), aid, reason, in.ClarificationMessage)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"claim": c})
}
