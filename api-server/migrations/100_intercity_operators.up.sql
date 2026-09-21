-- =====================================================
-- Migration 100: Intercity — operators, and the vehicle
-- types bus owners actually run
--
-- The market is bus owners who schedule journeys AND individuals who run
-- intercity movements themselves. Everyone is an OPERATOR: an individual is an
-- operator with one vehicle who is also its driver; a company is an operator
-- with several vehicles and several drivers. One flow, not two — the individual
-- is the degenerate case, never a separate code path.
--
-- Design: INTERCITY_DESIGN.md §15
-- =====================================================

CREATE TABLE IF NOT EXISTS intercity_operators (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id   UUID NOT NULL REFERENCES users(id),
    display_name    TEXT NOT NULL,          -- what the passenger sees
    kind            VARCHAR(12) NOT NULL,
    approval_status VARCHAR(20) NOT NULL DEFAULT 'PENDING_REVIEW',
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT operator_kind_valid CHECK (kind IN ('INDIVIDUAL', 'COMPANY')),
    CONSTRAINT operator_approval_valid CHECK (approval_status IN
        ('PENDING_REVIEW', 'APPROVED', 'REJECTED', 'SUSPENDED'))
);

-- One INDIVIDUAL operator per person. A company owner may hold several.
CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_operator_individual
    ON intercity_operators (owner_user_id)
    WHERE deleted_at IS NULL AND kind = 'INDIVIDUAL';

CREATE INDEX IF NOT EXISTS idx_intercity_operators_owner
    ON intercity_operators (owner_user_id) WHERE deleted_at IS NULL;

-- The roster: which drivers an operator may assign to a trip. An individual
-- operator has exactly one row here, pointing at themselves.
CREATE TABLE IF NOT EXISTS intercity_operator_drivers (
    operator_id       UUID NOT NULL REFERENCES intercity_operators(id),
    driver_profile_id UUID NOT NULL REFERENCES driver_profiles(id),
    deleted_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (operator_id, driver_profile_id)
);

CREATE INDEX IF NOT EXISTS idx_intercity_operator_drivers_driver
    ON intercity_operator_drivers (driver_profile_id) WHERE deleted_at IS NULL;

-- Who published the trip and is accountable for it. intercity_trips.driver_id
-- keeps its column but gains a precise meaning: the ASSIGNED driver, the person
-- who will actually drive. For an individual operator the two are the same
-- person. The availability gate (intercity_committed_until) is always written
-- for the ASSIGNED DRIVER, never the operator — an owner who does not drive was
-- never in city dispatch to begin with.
ALTER TABLE intercity_trips
    ADD COLUMN IF NOT EXISTS operator_id UUID REFERENCES intercity_operators(id);

-- Backfill: every existing trip was published by its driver acting as an
-- individual operator. Production has no intercity trips yet; this exists so
-- the migration is correct against development and staging data too.
INSERT INTO intercity_operators (owner_user_id, display_name, kind, approval_status)
SELECT DISTINCT dp.user_id,
       COALESCE(NULLIF(u.full_name, ''), 'Operator'),
       'INDIVIDUAL',
       'APPROVED'
  FROM intercity_trips t
  JOIN driver_profiles dp ON dp.id = t.driver_id
  JOIN users u ON u.id = dp.user_id
 WHERE t.operator_id IS NULL
ON CONFLICT DO NOTHING;

INSERT INTO intercity_operator_drivers (operator_id, driver_profile_id)
SELECT o.id, dp.id
  FROM intercity_trips t
  JOIN driver_profiles dp ON dp.id = t.driver_id
  JOIN intercity_operators o ON o.owner_user_id = dp.user_id
                            AND o.kind = 'INDIVIDUAL' AND o.deleted_at IS NULL
 WHERE t.operator_id IS NULL
ON CONFLICT DO NOTHING;

UPDATE intercity_trips t
   SET operator_id = o.id
  FROM driver_profiles dp, intercity_operators o
 WHERE t.driver_id = dp.id
   AND o.owner_user_id = dp.user_id
   AND o.kind = 'INDIVIDUAL'
   AND o.deleted_at IS NULL
   AND t.operator_id IS NULL;

ALTER TABLE intercity_trips ALTER COLUMN operator_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_intercity_trips_operator
    ON intercity_trips (operator_id, depart_at DESC) WHERE deleted_at IS NULL;

-- The vehicles bus owners actually run. total_seats was built capacity-agnostic
-- to 30 precisely so these need rows and not a schema change.
-- NOTE: credit_cost_rwf is a DEAD column — no money code reads it, and its
-- existing values invert per seat (CAB_TAXI 200 for 4 seats vs LIGHT_HILUX 100
-- for 6). Values here are placeholders for the NOT NULL; intercity charges one
-- ride credit per chargeable seat, from config.
INSERT INTO vehicle_types (code, display_name, base_fare_rwf, per_km_fare_rwf,
                           min_fare_rwf, max_passengers, credit_cost_rwf)
VALUES ('HIACE',   'Hiace Minibus',  1500, 400, 2000,  8, 100),
       ('COASTER', 'Coaster Bus',    2000, 350, 3000, 18, 100)
ON CONFLICT (code) DO NOTHING;

-- Per-booking seat cap: 096 hard-coded `seats BETWEEN 1 AND 4`, which was sized
-- for a 4-seat cab and is simply wrong for a bus — it blocks a family of five
-- on an 18-seat Coaster, which is an ordinary Kigali -> Musanze booking.
--
-- The anti-abuse goal is that no single account can take a WHOLE vehicle, and
-- that is a proportion, not an absolute. So the constraint now enforces only
-- what is structurally impossible (a booking larger than the largest vehicle),
-- and the proportional rule — LEAST(8, GREATEST(1, total_seats / 2)) — lives in
-- the service layer where it can see total_seats. A CHECK cannot: it has no
-- access to the parent trip.
ALTER TABLE intercity_bookings DROP CONSTRAINT IF EXISTS intercity_bookings_seats_check;
ALTER TABLE intercity_bookings ADD CONSTRAINT intercity_bookings_seats_check
    CHECK (seats BETWEEN 1 AND 30);

ANALYZE intercity_operators;
ANALYZE intercity_trips;
