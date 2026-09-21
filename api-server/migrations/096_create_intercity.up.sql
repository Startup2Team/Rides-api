-- =====================================================
-- Migration 096: Intercity — corridors, trips, bookings
--
-- A driver publishes a scheduled multi-seat trip; passengers book seats against
-- live availability. `rides` cannot be reused: it is strictly 1:1
-- (customer_id NOT NULL + a single driver_id) and intercity is 1:N.
--
-- Design: /Users/paccee/Pac/Rides/INTERCITY_DESIGN.md (v3)
-- =====================================================

-- Corridors are a lookup table, NOT free text. Matching on driver-typed names
-- would make "Kigali", "kigali" and "Kigali " three different corridors, and a
-- passenger searching a route that has trips would silently see an empty list.
CREATE TABLE IF NOT EXISTS intercity_corridors (
    code             VARCHAR(40) PRIMARY KEY,
    origin_name      TEXT        NOT NULL,
    destination_name TEXT        NOT NULL,
    is_active        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS intercity_trips (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id          UUID NOT NULL REFERENCES driver_profiles(id),
    vehicle_id         UUID NOT NULL REFERENCES driver_vehicles(id),
    corridor           VARCHAR(40) NOT NULL REFERENCES intercity_corridors(code),
    origin_name        TEXT NOT NULL,
    destination_name   TEXT NOT NULL,
    origin_point       GEOGRAPHY(POINT, 4326) NOT NULL,
    destination_point  GEOGRAPHY(POINT, 4326) NOT NULL,
    staging_address    TEXT NOT NULL,
    staging_point      GEOGRAPHY(POINT, 4326),
    depart_at          TIMESTAMPTZ NOT NULL,
    total_seats        SMALLINT NOT NULL CHECK (total_seats BETWEEN 1 AND 30),
    held_seats         SMALLINT NOT NULL DEFAULT 0,
    booked_seats       SMALLINT NOT NULL DEFAULT 0,
    price_per_seat_rwf INTEGER NOT NULL CHECK (price_per_seat_rwf BETWEEN 1 AND 500000),
    status             VARCHAR(20) NOT NULL DEFAULT 'OPEN',
    started_at         TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    cancelled_at       TIMESTAMPTZ,
    cancel_reason      TEXT,
    deleted_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- The backstop that makes an oversell UNPERSISTABLE even if application
    -- logic is later wrong. Seat acquisition is a single conditional UPDATE;
    -- this constraint is the guarantee behind it.
    CONSTRAINT seats_never_oversold CHECK (booked_seats + held_seats <= total_seats),
    CONSTRAINT seats_non_negative   CHECK (booked_seats >= 0 AND held_seats >= 0),
    -- PARTIALLY BOOKED / FULL are DERIVED, never stored: a stale FULL strands
    -- passengers, and two sources of truth for fullness will drift.
    CONSTRAINT trip_status_valid CHECK (status IN
        ('OPEN', 'BOARDING', 'IN_TRANSIT', 'COMPLETED', 'CANCELLED'))
);

CREATE INDEX IF NOT EXISTS idx_intercity_trips_search
    ON intercity_trips (corridor, depart_at)
    WHERE deleted_at IS NULL AND status = 'OPEN';

CREATE INDEX IF NOT EXISTS idx_intercity_trips_driver
    ON intercity_trips (driver_id, depart_at DESC)
    WHERE deleted_at IS NULL;

-- A driver cannot publish two departures in the same slot and sell the same
-- vehicle twice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_trip_driver_slot
    ON intercity_trips (driver_id, depart_at)
    WHERE deleted_at IS NULL AND status IN ('OPEN', 'BOARDING', 'IN_TRANSIT');

CREATE TABLE IF NOT EXISTS intercity_bookings (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id             UUID NOT NULL REFERENCES intercity_trips(id),
    customer_id         UUID NOT NULL REFERENCES users(id),
    -- Capped so no single account can take a whole vehicle. The app layer
    -- narrows this further to LEAST(4, GREATEST(1, total_seats / 2)).
    seats               SMALLINT NOT NULL CHECK (seats BETWEEN 1 AND 4),
    status              VARCHAR(20) NOT NULL,
    -- Snapshot: the driver may edit the trip price, but a booked passenger pays
    -- what they agreed to.
    price_per_seat_rwf  INTEGER NOT NULL,
    hold_expires_at     TIMESTAMPTZ,
    boarded_at          TIMESTAMPTZ,
    no_show_at          TIMESTAMPTZ,
    marked_by_driver_id UUID REFERENCES driver_profiles(id),
    cancelled_at        TIMESTAMPTZ,
    cancelled_by_role   VARCHAR(20),
    idempotency_key     VARCHAR(120),
    deleted_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT booking_status_valid CHECK (status IN
        ('HELD', 'CONFIRMED', 'BOARDED', 'COMPLETED', 'EXPIRED', 'CANCELLED', 'NO_SHOW')),
    -- A HELD row with a NULL expiry is invisible to the sweeper forever
    -- (`hold_expires_at < NOW()` is NULL-false), so its seats would never be
    -- released.
    CONSTRAINT hold_needs_expiry CHECK (status <> 'HELD' OR hold_expires_at IS NOT NULL)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_active_booking
    ON intercity_bookings (trip_id, customer_id)
    WHERE deleted_at IS NULL AND status IN ('HELD', 'CONFIRMED', 'BOARDED');

-- DELIBERATE EXCEPTION to the project-wide soft-delete rule: this index is NOT
-- partial on deleted_at. Idempotency must hold forever, independent of row
-- state — otherwise an offline client replaying its queue after a cancellation
-- consumes seats a second time. Precedent: migration 064's
-- uq_manual_payment_claims_idem.
CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_booking_idem
    ON intercity_bookings (customer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Postgres does not index FK columns. Every manifest read and every trip-cancel
-- fan-out would otherwise seq-scan the whole bookings table.
CREATE INDEX IF NOT EXISTS idx_intercity_bookings_trip
    ON intercity_bookings (trip_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_intercity_bookings_customer
    ON intercity_bookings (customer_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_intercity_bookings_hold_sweep
    ON intercity_bookings (hold_expires_at)
    WHERE deleted_at IS NULL AND status = 'HELD';

-- A brand-new table has no statistics and autovacuum may not analyze it before
-- the first production query.
ANALYZE intercity_trips;
ANALYZE intercity_bookings;
