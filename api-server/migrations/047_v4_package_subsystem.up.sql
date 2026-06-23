-- 039: v4 package subsystem (EXPAND step — additive, non-breaking).
-- Creates versioned catalog, campaigns, immutable purchase snapshots, an
-- append-only entitlement ledger + balance cache, and an admin audit log.
-- Old tables (driver_ride_credits, bonus_*) are left in place until the
-- CONTRACT migration after the Go layer cuts over.

-- ── Stable code on the existing catalog (identity key) ────────────────────────
ALTER TABLE ride_packages ADD COLUMN IF NOT EXISTS code VARCHAR(40);
UPDATE ride_packages
   SET code = lower(regexp_replace(name, '\s+', '_', 'g'))
 WHERE code IS NULL OR code = '';

-- ── Immutable offer values (create a new version to change an offer) ──────────
CREATE TABLE IF NOT EXISTS ride_package_versions (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id        uuid        NOT NULL REFERENCES ride_packages(id) ON DELETE CASCADE,
    version_number    integer     NOT NULL,
    rides             integer     NOT NULL,
    bonus_rides       integer     NOT NULL DEFAULT 0,
    is_unlimited      boolean     NOT NULL DEFAULT FALSE,
    price_rwf         integer     NOT NULL,
    cost_per_ride_rwf integer     NOT NULL DEFAULT 30,
    validity_days     integer     NOT NULL DEFAULT 30,
    is_promotional    boolean     NOT NULL DEFAULT FALSE,
    status            varchar(12) NOT NULL DEFAULT 'DRAFT', -- DRAFT|SCHEDULED|ACTIVE|ARCHIVED
    active_from       timestamptz,
    active_until      timestamptz,
    created_by        uuid,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_pkg_version UNIQUE (package_id, version_number)
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_active_version
    ON ride_package_versions (package_id) WHERE status = 'ACTIVE';

-- ── Campaigns (resolution overrides; folds in old bonus tiers) ───────────────
CREATE TABLE IF NOT EXISTS campaigns (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    code                   varchar(40) UNIQUE NOT NULL,
    name                   varchar(100) NOT NULL,
    type                   varchar(20) NOT NULL, -- GLOBAL|VEHICLE_TYPE|PACKAGE|FIRST_PURCHASE|REFERRAL
    status                 varchar(12) NOT NULL DEFAULT 'DRAFT', -- DRAFT|SCHEDULED|ACTIVE|EXPIRED|ARCHIVED
    starts_at              timestamptz,
    ends_at                timestamptz,
    target_vehicle_type_id uuid REFERENCES vehicle_types(id),
    target_package_id      uuid REFERENCES ride_packages(id),
    override_price_rwf     integer,
    override_rides         integer,
    override_bonus_rides   integer,
    priority               integer     NOT NULL DEFAULT 0,
    max_redemptions        integer,
    per_driver_limit       integer,
    created_by             uuid,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_campaigns_window ON campaigns (status, starts_at, ends_at);

-- ── Purchase = immutable snapshot + MoMo lifecycle ───────────────────────────
CREATE TABLE IF NOT EXISTS package_purchases (
    id                     uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id              uuid        NOT NULL REFERENCES driver_profiles(id),
    vehicle_id             uuid        REFERENCES driver_vehicles(id),
    vehicle_type_id        uuid        NOT NULL REFERENCES vehicle_types(id),
    -- frozen snapshot (plain values, never live FKs)
    package_id             uuid        NOT NULL,
    package_version_id     uuid        NOT NULL,
    package_version_number integer     NOT NULL,
    package_name           varchar(60) NOT NULL,
    campaign_id            uuid,
    campaign_code          varchar(40),
    price_paid_rwf         integer     NOT NULL,
    rides_granted          integer     NOT NULL,
    bonus_rides_granted    integer     NOT NULL DEFAULT 0,
    is_unlimited           boolean     NOT NULL DEFAULT FALSE,
    -- payment (MoMo webhook)
    status                 varchar(12) NOT NULL DEFAULT 'PENDING', -- PENDING|PAID|FAILED|CANCELLED|EXPIRED
    payment_provider       varchar(10),
    payment_phone          varchar(20),
    payment_ref            varchar(100) UNIQUE NOT NULL,
    provider_txn_id        varchar(100),
    idempotency_key        varchar(100) UNIQUE NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    paid_at                timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_purchases_driver       ON package_purchases (driver_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_purchases_status       ON package_purchases (status);
CREATE INDEX IF NOT EXISTS idx_purchases_provider_txn ON package_purchases (provider_txn_id) WHERE provider_txn_id IS NOT NULL;

-- ── Entitlement ledger (append-only; balance is derived) ─────────────────────
CREATE TABLE IF NOT EXISTS ride_credit_ledger (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id          uuid        NOT NULL REFERENCES driver_profiles(id),
    vehicle_id         uuid        REFERENCES driver_vehicles(id),
    vehicle_type_id    uuid        NOT NULL REFERENCES vehicle_types(id),
    entry_type         varchar(20) NOT NULL, -- PURCHASE_GRANT|BONUS_GRANT|RIDE_DEDUCTION|RIDE_REFUND|ADMIN_GRANT|ADMIN_REVOKE|EXPIRY
    rides_delta        integer     NOT NULL DEFAULT 0,
    bonus_delta        integer     NOT NULL DEFAULT 0,
    balance_rides      integer     NOT NULL,
    balance_bonus      integer     NOT NULL,
    source_purchase_id uuid        REFERENCES package_purchases(id),
    source_ride_id     uuid        REFERENCES rides(id),
    admin_id           uuid,
    reason             text,
    idempotency_key    varchar(100) UNIQUE,
    expires_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ledger_balance ON ride_credit_ledger (driver_id, vehicle_type_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_time    ON ride_credit_ledger USING brin (created_at);

-- ── Fast balance cache (updated only inside a ledger transaction) ─────────────
CREATE TABLE IF NOT EXISTS driver_entitlements (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id       uuid        NOT NULL REFERENCES driver_profiles(id),
    vehicle_id      uuid        REFERENCES driver_vehicles(id),
    vehicle_type_id uuid        NOT NULL REFERENCES vehicle_types(id),
    rides_remaining integer     NOT NULL DEFAULT 0,
    bonus_remaining integer     NOT NULL DEFAULT 0,
    unlimited_until timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_entitlement UNIQUE (driver_id, vehicle_type_id)
);

-- ── Admin audit (every admin write) ──────────────────────────────────────────
CREATE TABLE IF NOT EXISTS admin_audit_log (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_admin_id uuid        NOT NULL,
    action         varchar(50) NOT NULL,
    entity_type    varchar(30) NOT NULL,
    entity_id      uuid,
    before         jsonb,
    after          jsonb,
    reason         text,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_time   ON admin_audit_log USING brin (created_at);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON admin_audit_log (entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_actor  ON admin_audit_log (actor_admin_id);

-- ── Backfill: one ACTIVE version per existing package ─────────────────────────
INSERT INTO ride_package_versions
    (package_id, version_number, rides, bonus_rides, price_rwf, cost_per_ride_rwf, validity_days, is_promotional, status, active_from)
SELECT id, 1, ride_count, 0, price_rwf, COALESCE(cost_per_ride_rwf, 30), validity_days, is_promotional, 'ACTIVE', now()
FROM ride_packages
ON CONFLICT (package_id, version_number) DO NOTHING;

-- Align the seeded packages with the mobile demo split (rides + bonus).
UPDATE ride_package_versions v SET rides = 30,  bonus_rides = 5
  FROM ride_packages p WHERE v.package_id = p.id AND v.version_number = 1 AND p.name = 'Launch Starter Package';
UPDATE ride_package_versions v SET rides = 60,  bonus_rides = 15
  FROM ride_packages p WHERE v.package_id = p.id AND v.version_number = 1 AND p.name = 'Growth Package';
UPDATE ride_package_versions v SET rides = 120, bonus_rides = 30
  FROM ride_packages p WHERE v.package_id = p.id AND v.version_number = 1 AND p.name = 'Pro Package';
