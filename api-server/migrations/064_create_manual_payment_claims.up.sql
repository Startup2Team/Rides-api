-- 064: driver manual package-payment claims (claim-centric state machine:
-- created -> submitted -> approved/rejected/expired/cancelled), with an audit
-- log. Complements the existing purchase-centric flow. See
-- docs/backend/MOBILE_PAYMENT_CONTRACTS.md (Flow J).

CREATE TABLE IF NOT EXISTS manual_payment_claims (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version                INTEGER NOT NULL DEFAULT 1, -- optimistic-concurrency counter
    user_id                UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- owner (driver)
    driver_id              TEXT NOT NULL,
    vehicle_id             TEXT NOT NULL,
    vehicle_type           TEXT NOT NULL,
    offer_id               TEXT NOT NULL,
    package_id             TEXT NOT NULL,
    package_version        TEXT NOT NULL,
    package_name           TEXT NOT NULL,
    expected_amount_rwf    BIGINT NOT NULL,
    provider               VARCHAR(16) NOT NULL, -- 'mtn' | 'airtel'
    merchant_code_snapshot TEXT NOT NULL,
    payer_phone_number     TEXT NOT NULL,
    transaction_reference  TEXT,
    proof_image_id         TEXT,
    status                 VARCHAR(16) NOT NULL DEFAULT 'created',
    idempotency_key        VARCHAR(160) NOT NULL,
    rejection_reason       TEXT,
    clarification_message  TEXT,
    support_note           TEXT,
    reviewed_by            UUID,
    activation_id          TEXT,
    purchase_transaction_id TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at           TIMESTAMPTZ,
    expires_at             TIMESTAMPTZ NOT NULL,
    reviewed_at            TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_manual_payment_claims_user
    ON manual_payment_claims (user_id, created_at DESC);

-- Idempotent create: a repeated key for the same user returns the existing claim.
CREATE UNIQUE INDEX IF NOT EXISTS uq_manual_payment_claims_idem
    ON manual_payment_claims (user_id, idempotency_key);

CREATE TABLE IF NOT EXISTS manual_payment_claim_audit (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    claim_id    UUID NOT NULL REFERENCES manual_payment_claims(id) ON DELETE CASCADE,
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_type  VARCHAR(16) NOT NULL, -- 'driver' | 'admin' | 'system'
    actor_id    TEXT,
    action      VARCHAR(32) NOT NULL, -- 'created' | 'submitted' | 'resubmitted' | 'cancelled' | ...
    reason_code TEXT
);

CREATE INDEX IF NOT EXISTS idx_manual_payment_claim_audit_claim
    ON manual_payment_claim_audit (claim_id, at ASC);
