package packagepayments

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const claimCols = `
	id, version, driver_id, vehicle_id, vehicle_type, offer_id, package_id,
	package_version, package_name, expected_amount_rwf, provider,
	merchant_code_snapshot, payer_phone_number, transaction_reference,
	proof_image_id, status, created_at, submitted_at, expires_at, reviewed_at,
	reviewed_by, rejection_reason, clarification_message, support_note,
	activation_id, purchase_transaction_id, updated_at, idempotency_key`

func scanClaim(row pgx.Row) (*Claim, error) {
	c := &Claim{}
	if err := row.Scan(
		&c.ID, &c.Version, &c.DriverID, &c.VehicleID, &c.VehicleType, &c.OfferID, &c.PackageID,
		&c.PackageVersion, &c.PackageName, &c.ExpectedAmountRwf, &c.Provider,
		&c.MerchantCodeSnapshot, &c.PayerPhoneNumber, &c.TransactionReference,
		&c.ProofImageID, &c.Status, &c.CreatedAt, &c.SubmittedAt, &c.ExpiresAt, &c.ReviewedAt,
		&c.ReviewedBy, &c.RejectionReason, &c.ClarificationMessage, &c.SupportNote,
		&c.ActivationID, &c.PurchaseTransactionID, &c.UpdatedAt, &c.IdempotencyKey,
	); err != nil {
		return nil, err
	}
	return c, nil
}

// Insert creates a claim. On idempotency-key conflict the existing claim is
// returned instead (dedupe), so retries never create duplicates.
func (r *Repository) Insert(ctx context.Context, userID string, c *Claim) (*Claim, error) {
	created, err := scanClaim(r.db.QueryRow(ctx, `
		INSERT INTO manual_payment_claims (
			user_id, driver_id, vehicle_id, vehicle_type, offer_id, package_id,
			package_version, package_name, expected_amount_rwf, provider,
			merchant_code_snapshot, payer_phone_number, transaction_reference,
			proof_image_id, status, idempotency_key, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING `+claimCols,
		userID, c.DriverID, c.VehicleID, c.VehicleType, c.OfferID, c.PackageID,
		c.PackageVersion, c.PackageName, c.ExpectedAmountRwf, c.Provider,
		c.MerchantCodeSnapshot, c.PayerPhoneNumber, c.TransactionReference,
		c.ProofImageID, c.Status, c.IdempotencyKey, c.ExpiresAt,
	))
	if err == pgx.ErrNoRows {
		// Conflict: return the previously created claim.
		return r.FindByIdempotencyKey(ctx, userID, c.IdempotencyKey)
	}
	return created, err
}

func (r *Repository) FindByID(ctx context.Context, userID, id string) (*Claim, error) {
	return scanClaim(r.db.QueryRow(ctx,
		`SELECT `+claimCols+` FROM manual_payment_claims WHERE user_id = $1 AND id = $2`, userID, id))
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, userID, key string) (*Claim, error) {
	return scanClaim(r.db.QueryRow(ctx,
		`SELECT `+claimCols+` FROM manual_payment_claims WHERE user_id = $1 AND idempotency_key = $2`, userID, key))
}

func (r *Repository) List(ctx context.Context, userID string, limit int) ([]*Claim, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+claimCols+` FROM manual_payment_claims
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
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

// ExpireStale flags the caller's created/submitted claims past their expiry.
func (r *Repository) ExpireStale(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE manual_payment_claims
		 SET status = 'expired', version = version + 1, updated_at = NOW()
		 WHERE user_id = $1 AND status IN ('created','submitted') AND expires_at < NOW()`, userID)
	return err
}

// UpdateStatus applies a state transition and bumps the version. Optional
// columns (submitted_at, rejection fields) are set via the flags.
func (r *Repository) UpdateStatus(ctx context.Context, userID, id, status string, setSubmitted, clearRejection bool) (*Claim, error) {
	return scanClaim(r.db.QueryRow(ctx, `
		UPDATE manual_payment_claims SET
			status       = $3,
			version      = version + 1,
			submitted_at = CASE WHEN $4 THEN NOW() ELSE submitted_at END,
			rejection_reason      = CASE WHEN $5 THEN NULL ELSE rejection_reason END,
			clarification_message = CASE WHEN $5 THEN NULL ELSE clarification_message END,
			updated_at   = NOW()
		WHERE user_id = $1 AND id = $2
		RETURNING `+claimCols,
		userID, id, status, setSubmitted, clearRejection))
}

func (r *Repository) AddAudit(ctx context.Context, claimID, actorType string, actorID *string, action string, reasonCode *string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO manual_payment_claim_audit (claim_id, actor_type, actor_id, action, reason_code)
		 VALUES ($1,$2,$3,$4,$5)`, claimID, actorType, actorID, action, reasonCode)
	return err
}

func (r *Repository) AuditLog(ctx context.Context, claimID string) ([]AuditEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, at, actor_type, actor_id, action, reason_code
		 FROM manual_payment_claim_audit WHERE claim_id = $1 ORDER BY at ASC`, claimID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	log := make([]AuditEntry, 0)
	for rows.Next() {
		e := AuditEntry{}
		if err := rows.Scan(&e.ID, &e.At, &e.ActorType, &e.ActorID, &e.Action, &e.ReasonCode); err != nil {
			return nil, err
		}
		log = append(log, e)
	}
	return log, rows.Err()
}
