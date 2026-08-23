CREATE TABLE waitlist_signups (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    role              VARCHAR(20) NOT NULL,
    name              TEXT        NOT NULL,
    phone             TEXT        NOT NULL,
    email             TEXT,
    area              TEXT,
    vehicle_type      VARCHAR(20),
    referral_code     TEXT UNIQUE,
    referred_by       TEXT,
    consent_launch    BOOLEAN     NOT NULL DEFAULT false,
    consent_marketing BOOLEAN     NOT NULL DEFAULT false,
    source            TEXT,
    opted_out_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Dedupe: a person resubmitting the same role+phone is an idempotent
    -- no-op at the application layer, not a new row.
    CONSTRAINT waitlist_signups_role_phone_key UNIQUE (role, phone)
);

CREATE INDEX idx_waitlist_signups_created_at ON waitlist_signups (created_at DESC);
CREATE INDEX idx_waitlist_signups_area       ON waitlist_signups (area);
-- No separate index on referral_code: the UNIQUE constraint above already
-- creates one (a duplicate would be pure waste).
