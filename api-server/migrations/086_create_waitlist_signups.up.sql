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
    CONSTRAINT waitlist_signups_role_phone_key UNIQUE (role, phone),

    CONSTRAINT waitlist_signups_role_check CHECK (role IN ('CUSTOMER', 'DRIVER')),
    CONSTRAINT waitlist_signups_vehicle_type_check CHECK (
        vehicle_type IS NULL OR vehicle_type IN ('MOTO_BIKE', 'CAB_TAXI', 'HEAVY_FUSO', 'LIGHT_HILUX', 'TUK_TUK')
    )
);

CREATE INDEX idx_waitlist_signups_created_at ON waitlist_signups (created_at DESC);
-- No index on `area`: the admin console searches it with a leading-wildcard
-- ILIKE ('%term%'), which a btree cannot serve (confirmed via EXPLAIN) — an
-- index here would only add write overhead for zero read benefit.
-- No separate index on referral_code: the UNIQUE constraint above already
-- creates one (a duplicate would be pure waste).
