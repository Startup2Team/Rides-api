-- =====================================================
-- Migration 033: Bonus system
-- Admin-configurable purchase bonuses + registration
-- free-ride grant tracking.
-- =====================================================

-- ─── bonus_tiers ─────────────────────────────────────────────────────────────
-- Admin defines what bonus each purchase number earns.
-- purchase_number = 1 → first ever package purchase gets bonus_rides extra.
-- purchase_number = NULL → applies to every purchase beyond the last explicit tier.

CREATE TABLE bonus_tiers (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name             VARCHAR(100) NOT NULL,                        -- "First Purchase Bonus"
    description      TEXT,                                         -- shown to driver in app
    trigger_type     VARCHAR(20)  NOT NULL DEFAULT 'PURCHASE_COUNT',  -- PURCHASE_COUNT | REGISTRATION
    purchase_number  INTEGER,                                      -- NULL = any purchase (catch-all)
    bonus_rides      INTEGER      NOT NULL CHECK (bonus_rides > 0),
    vehicle_type_id  UUID         REFERENCES vehicle_types(id),   -- NULL = applies to all vehicle types
    is_active        BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Seed default tiers (admin can edit/delete/add via API):
--   Purchase 1 → +10 bonus rides
--   Purchase 2 → +20 bonus rides
--   Purchase 3+ → +30 bonus rides (catch-all, purchase_number IS NULL)
INSERT INTO bonus_tiers (name, description, trigger_type, purchase_number, bonus_rides) VALUES
    ('First Purchase Bonus',  'Welcome gift — +10 rides free on your first package!',   'PURCHASE_COUNT', 1,    10),
    ('Second Purchase Bonus', 'Keep riding — +20 extra rides on your second package!',  'PURCHASE_COUNT', 2,    20),
    ('Loyal Rider Bonus',     'You''re loyal! Every purchase from now gives +30 rides.', 'PURCHASE_COUNT', NULL, 30);

-- Registration free-ride tier (trigger_type = REGISTRATION, granted on driver approval)
INSERT INTO bonus_tiers (name, description, trigger_type, purchase_number, bonus_rides) VALUES
    ('Registration Welcome',  '30 free rides to get you started on Taravelis!', 'REGISTRATION', NULL, 30);

-- ─── bonus_grants ─────────────────────────────────────────────────────────────
-- Immutable record of every bonus that was issued.
-- Prevents double-granting and provides admin audit trail.

CREATE TABLE bonus_grants (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id           UUID         NOT NULL REFERENCES users(id),
    tier_id             UUID         NOT NULL REFERENCES bonus_tiers(id),
    trigger_credit_id   UUID         REFERENCES driver_ride_credits(id), -- which purchase triggered this (NULL for REGISTRATION)
    vehicle_type_id     UUID         NOT NULL REFERENCES vehicle_types(id),
    bonus_rides         INTEGER      NOT NULL CHECK (bonus_rides > 0),
    expires_at          TIMESTAMPTZ  NOT NULL,                           -- inherits validity from triggering package or 30 days
    granted_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_bonus_grants_driver     ON bonus_grants (driver_id, granted_at DESC);
CREATE INDEX idx_bonus_grants_tier       ON bonus_grants (tier_id);
CREATE INDEX idx_bonus_grants_credit     ON bonus_grants (trigger_credit_id) WHERE trigger_credit_id IS NOT NULL;

-- One REGISTRATION bonus per driver: registration grants have no trigger_credit_id.
-- Partial index prevents duplicate registration bonuses (subqueries not allowed in predicates).
CREATE UNIQUE INDEX uniq_bonus_registration ON bonus_grants (driver_id, tier_id)
    WHERE trigger_credit_id IS NULL;

-- ─── purchase_count helper view ───────────────────────────────────────────────
-- Counts non-promotional paid purchases per driver, used by bonus engine.
CREATE VIEW driver_purchase_counts AS
    SELECT
        drc.driver_id,
        COUNT(*) AS total_purchases
    FROM driver_ride_credits drc
    WHERE drc.is_promotional = FALSE
    GROUP BY drc.driver_id;

-- ─── Expand free_trial_used → registration_bonus_granted ─────────────────────
-- Keep free_trial_used for backward-compat but add clearer column.
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS registration_bonus_granted BOOLEAN NOT NULL DEFAULT FALSE;

-- Mark existing drivers who already received a free trial as having the bonus.
UPDATE driver_profiles SET registration_bonus_granted = free_trial_used;
