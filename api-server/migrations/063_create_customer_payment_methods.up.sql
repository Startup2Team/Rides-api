-- 063: customer-managed payment methods (MoMo / cash). The backend previously
-- stored no per-customer methods (it took a momo_phone at purchase time); this
-- adds a managed list so the mobile payments screen can list / add / default /
-- delete methods. See docs/backend/MOBILE_PAYMENT_CONTRACTS.md (Flow F).

CREATE TABLE IF NOT EXISTS customer_payment_methods (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider     VARCHAR(16) NOT NULL, -- 'mtn' | 'airtel' | 'cash'
    label        VARCHAR(120) NOT NULL,
    phone_number VARCHAR(20),          -- nullable (cash has none)
    is_default   BOOLEAN NOT NULL DEFAULT FALSE,
    -- Dedupe double-taps / retries of the create call.
    idempotency_key VARCHAR(120),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customer_payment_methods_user
    ON customer_payment_methods (user_id);

-- At most one default method per user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_payment_methods_default
    ON customer_payment_methods (user_id)
    WHERE is_default;

-- Idempotent create: a repeated idempotency_key for the same user is a no-op.
CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_payment_methods_idem
    ON customer_payment_methods (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
