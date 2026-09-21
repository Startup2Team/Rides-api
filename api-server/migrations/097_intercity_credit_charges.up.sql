-- =====================================================
-- Migration 097: Intercity — credit charge obligations
--
-- Credits are charged at DEPARTURE, one per chargeable seat. Per-seat charging
-- is N calls to packages.deductOne, each in its own transaction, so NO point in
-- the request path can make the charge atomic with the seat move. Instead the
-- departure transaction records a durable OBLIGATION, and an idempotent worker
-- settles it against the v4 entitlement ledger afterwards.
-- =====================================================

-- ride_credit_ledger.source_ride_id is REFERENCES rides(id); an intercity
-- booking id cannot be stored there. Additive column, NULL for city rides.
ALTER TABLE ride_credit_ledger
    ADD COLUMN IF NOT EXISTS source_intercity_trip_id UUID REFERENCES intercity_trips(id);

-- A ledger row must name exactly one source, or admin/reporting silently picks
-- whichever it reads first. NOT VALID avoids a blocking scan; existing rows are
-- all single-source. House style: see migrations 082, 085.
ALTER TABLE ride_credit_ledger
    DROP CONSTRAINT IF EXISTS one_source_only;
ALTER TABLE ride_credit_ledger
    ADD CONSTRAINT one_source_only
    CHECK (num_nonnulls(source_ride_id, source_purchase_id, source_intercity_trip_id) <= 1)
    NOT VALID;

CREATE INDEX IF NOT EXISTS idx_rcl_intercity
    ON ride_credit_ledger (source_intercity_trip_id)
    WHERE source_intercity_trip_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS intercity_credit_charges (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id            UUID NOT NULL REFERENCES intercity_trips(id),
    driver_id          UUID NOT NULL REFERENCES driver_profiles(id),
    -- Balances are held per (driver, vehicle_type). The pool is FROZEN here at
    -- departure: re-deriving it at settlement time would let a driver who
    -- switched or deactivated a vehicle be charged against a different pool
    -- than the publish gate checked, or fail permanently into false arrears.
    vehicle_type_id    UUID NOT NULL REFERENCES vehicle_types(id),
    seq                SMALLINT NOT NULL,
    -- 'intercity:' || trip_id || ':' || seq = 49 chars; flows into
    -- ride_credit_ledger.idempotency_key, which is varchar(100).
    idempotency_key    VARCHAR(100) NOT NULL,
    status             VARCHAR(12) NOT NULL DEFAULT 'PENDING',
    attempts           INTEGER NOT NULL DEFAULT 0,
    -- Only insufficient-credit failures may promote to ARREARS. Five transient
    -- errors must never write off a collectable debt.
    no_credit_attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until       TIMESTAMPTZ,
    last_error         TEXT,
    charged_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT icc_status_valid CHECK (status IN ('PENDING', 'CHARGED', 'ARREARS'))
);

-- NOTE: this table deliberately has NO deleted_at, against the project-wide
-- soft-delete rule. A financial obligation is immutable — soft-deleting a debt
-- is not a coherent operation, and it would let an admin tool silently erase
-- money. It also keeps both unique indexes NON-partial, which matters: with a
-- partial unique index, `ON CONFLICT (trip_id, seq) DO NOTHING` raises "there is
-- no unique or exclusion constraint matching the ON CONFLICT specification" —
-- a deterministic 500 on every departure.
CREATE UNIQUE INDEX IF NOT EXISTS uq_icc_trip_seq ON intercity_credit_charges (trip_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS uq_icc_idem     ON intercity_credit_charges (idempotency_key);

-- Worker claim order, with backoff so a broke driver's rows do not hot-loop and
-- contend the same driver_entitlements row the city-ride deduct path needs.
CREATE INDEX IF NOT EXISTS idx_icc_due
    ON intercity_credit_charges (next_attempt_at)
    WHERE status = 'PENDING';

-- Supports the publish gate's "does this driver owe anything?" lookup.
CREATE INDEX IF NOT EXISTS idx_icc_driver
    ON intercity_credit_charges (driver_id, status);

ANALYZE intercity_credit_charges;
