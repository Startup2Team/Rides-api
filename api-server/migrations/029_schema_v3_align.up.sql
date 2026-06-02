-- =====================================================
-- Migration 029: Schema v3 alignment
-- Adds missing tables and columns from the Taravelis
-- v3 schema spec to the existing database.
-- =====================================================

-- ─── users: new v3 columns ───────────────────────────────────────────────────

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS profile_image_url  TEXT,
    ADD COLUMN IF NOT EXISTS current_mode       VARCHAR(10)   DEFAULT 'customer',
    ADD COLUMN IF NOT EXISTS suspension_reason  TEXT,
    ADD COLUMN IF NOT EXISTS last_seen_at       TIMESTAMPTZ;

-- ─── driver_profiles: new v3 columns ─────────────────────────────────────────

ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS rating                     DECIMAL(3,2)  NOT NULL DEFAULT 5.0,
    ADD COLUMN IF NOT EXISTS total_earnings_rwf         BIGINT        NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cancellation_rate          DECIMAL(5,2)  NOT NULL DEFAULT 0.0,
    ADD COLUMN IF NOT EXISTS daily_declines             INTEGER       NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS suspicious_disconnect_count INTEGER      NOT NULL DEFAULT 0;

-- ─── customer_profiles ───────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS customer_profiles (
    user_id           UUID         PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    total_rides       INTEGER      NOT NULL DEFAULT 0,
    completed_rides   INTEGER      NOT NULL DEFAULT 0,
    cancelled_rides   INTEGER      NOT NULL DEFAULT 0,
    rating            DECIMAL(3,2) NOT NULL DEFAULT 5.0,
    preferred_payment VARCHAR(10)  NOT NULL DEFAULT 'cash',
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Back-fill a customer_profile row for every existing user so new users
-- always get a row via the register trigger below.
INSERT INTO customer_profiles (user_id)
SELECT id FROM users
ON CONFLICT (user_id) DO NOTHING;

-- Auto-create a customer_profile on every new user registration.
CREATE OR REPLACE FUNCTION create_customer_profile()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO customer_profiles (user_id) VALUES (NEW.id) ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_create_customer_profile ON users;
CREATE TRIGGER trg_create_customer_profile
    AFTER INSERT ON users
    FOR EACH ROW EXECUTE FUNCTION create_customer_profile();

-- ─── driver_vehicles ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS driver_vehicles (
    id               UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id        UUID         NOT NULL REFERENCES driver_profiles(id) ON DELETE CASCADE,
    vehicle_type_id  UUID         NOT NULL REFERENCES vehicle_types(id),
    plate_number     VARCHAR(20)  UNIQUE NOT NULL,
    make             VARCHAR(50),
    model            VARCHAR(50),
    year             INTEGER,
    color            VARCHAR(30),
    passenger_seats  INTEGER,
    load_capacity_kg DECIMAL(8,2),
    is_active        BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vehicles_driver        ON driver_vehicles(driver_id);
CREATE INDEX IF NOT EXISTS idx_vehicles_driver_active ON driver_vehicles(driver_id, is_active);
CREATE INDEX IF NOT EXISTS idx_vehicles_type          ON driver_vehicles(vehicle_type_id);

-- ─── hot_zones ────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS hot_zones (
    id            UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    name          VARCHAR(100)  NOT NULL,
    category      VARCHAR(30)   NOT NULL,
    lat           DECIMAL(10,7) NOT NULL,
    lng           DECIMAL(10,7) NOT NULL,
    geohash6      VARCHAR(6)    NOT NULL,
    radius_meters INTEGER       NOT NULL DEFAULT 500,
    city          VARCHAR(50)   NOT NULL DEFAULT 'Kigali',
    is_active     BOOLEAN       NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_hot_zones_geohash     ON hot_zones(geohash6);
CREATE INDEX IF NOT EXISTS idx_hot_zones_city_active ON hot_zones(city, is_active);

-- ─── zone_demand_stats ───────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS zone_demand_stats (
    geohash6            VARCHAR(6)  NOT NULL,
    vehicle_type_id     UUID        NOT NULL REFERENCES vehicle_types(id),
    hour_bucket         TIMESTAMPTZ NOT NULL,
    ride_request_count  INTEGER     NOT NULL DEFAULT 0,
    completed_count     INTEGER     NOT NULL DEFAULT 0,
    avg_wait_seconds    INTEGER,
    PRIMARY KEY (geohash6, vehicle_type_id, hour_bucket)
);

CREATE INDEX IF NOT EXISTS idx_zone_demand_geo_hour ON zone_demand_stats(geohash6, hour_bucket);

-- ─── payments ────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS payments (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id               UUID        UNIQUE NOT NULL REFERENCES rides(id),
    payer_id              UUID        NOT NULL REFERENCES users(id),
    receiver_id           UUID        NOT NULL REFERENCES users(id),
    amount_rwf            INTEGER     NOT NULL,
    platform_fee_rwf      INTEGER     NOT NULL DEFAULT 0,
    driver_amount_rwf     INTEGER     NOT NULL,
    payment_method        VARCHAR(10) NOT NULL DEFAULT 'cash',
    momo_provider         VARCHAR(10),
    transaction_reference VARCHAR,
    status                VARCHAR(15) NOT NULL DEFAULT 'PENDING',
    paid_at               TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payments_payer    ON payments(payer_id);
CREATE INDEX IF NOT EXISTS idx_payments_receiver ON payments(receiver_id);

-- ─── wallets ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS wallets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID        UNIQUE NOT NULL REFERENCES users(id),
    balance_rwf   BIGINT      NOT NULL DEFAULT 0,
    currency      VARCHAR(5)  NOT NULL DEFAULT 'RWF',
    is_frozen     BOOLEAN     NOT NULL DEFAULT FALSE,
    freeze_reason TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── wallet_transactions ─────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS wallet_transactions (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id      UUID        NOT NULL REFERENCES wallets(id),
    type           VARCHAR(25) NOT NULL,
    amount_rwf     INTEGER     NOT NULL,
    balance_before BIGINT      NOT NULL,
    balance_after  BIGINT      NOT NULL,
    status         VARCHAR(15) NOT NULL DEFAULT 'COMPLETED',
    reference_type VARCHAR(20),
    reference_id   UUID,
    description    TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wallet_txn_history ON wallet_transactions(wallet_id, created_at);
CREATE INDEX IF NOT EXISTS idx_wallet_txn_time    ON wallet_transactions USING BRIN(created_at);

-- ─── ratings ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS ratings (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id    UUID        NOT NULL REFERENCES rides(id),
    rater_id   UUID        NOT NULL REFERENCES users(id),
    rated_id   UUID        NOT NULL REFERENCES users(id),
    direction  VARCHAR(25) NOT NULL,
    score      SMALLINT    NOT NULL CHECK (score BETWEEN 1 AND 5),
    comment    TEXT,
    tags       TEXT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(ride_id, rater_id)
);

CREATE INDEX IF NOT EXISTS idx_ratings_rated ON ratings(rated_id);

-- ─── ride_disputes ───────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS ride_disputes (
    id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    ride_id           UUID          NOT NULL REFERENCES rides(id),
    raised_by_id      UUID          NOT NULL REFERENCES users(id),
    raised_by_role    VARCHAR(10)   NOT NULL,
    dispute_type      VARCHAR(30)   NOT NULL,
    description       TEXT,
    driver_last_lat   DECIMAL(10,7),
    driver_last_lng   DECIMAL(10,7),
    customer_last_lat DECIMAL(10,7),
    customer_last_lng DECIMAL(10,7),
    status            VARCHAR(15)   NOT NULL DEFAULT 'OPEN',
    resolution        VARCHAR(30),
    resolved_by       UUID          REFERENCES users(id),
    resolved_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_disputes_ride_id ON ride_disputes(ride_id);
CREATE INDEX IF NOT EXISTS idx_disputes_open    ON ride_disputes(status, created_at) WHERE status = 'OPEN';

-- ─── notifications ───────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS notifications (
    id       UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id  UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title    VARCHAR(100) NOT NULL,
    body     TEXT         NOT NULL,
    type     VARCHAR(20)  NOT NULL,
    data     JSONB,
    is_read  BOOLEAN      NOT NULL DEFAULT FALSE,
    sent_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    read_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_notif_unread ON notifications(user_id, is_read, sent_at) WHERE is_read = FALSE;
CREATE INDEX IF NOT EXISTS idx_notif_time   ON notifications USING BRIN(sent_at);

-- ─── driver_sessions ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS driver_sessions (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id            UUID        NOT NULL REFERENCES driver_profiles(id),
    vehicle_id           UUID        REFERENCES driver_vehicles(id),
    started_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at             TIMESTAMPTZ,
    total_online_minutes INTEGER     NOT NULL DEFAULT 0,
    rides_completed      INTEGER     NOT NULL DEFAULT 0,
    earnings_rwf         BIGINT      NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sessions_driver         ON driver_sessions(driver_id);
CREATE INDEX IF NOT EXISTS idx_sessions_history        ON driver_sessions(driver_id, started_at);
