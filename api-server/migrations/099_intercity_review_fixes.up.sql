-- =====================================================
-- Migration 099: Intercity — review fixes for 096/097
--
-- Findings from the schema review of commit 8c2e030.
-- =====================================================

-- (1) The booking idempotency index was partial on `idempotency_key IS NOT NULL`,
-- which means `ON CONFLICT (customer_id, idempotency_key) DO NOTHING` cannot
-- infer it and raises, at PLAN time:
--     ERROR: there is no unique or exclusion constraint matching the ON CONFLICT
--            specification
-- That is a deterministic 500 on every hold, not a data-dependent one — the same
-- trap 097 deliberately avoided on intercity_credit_charges, reintroduced one
-- table over.
--
-- The predicate bought nothing: in a unique btree NULL is never equal to NULL,
-- so a NON-partial unique index on (customer_id, idempotency_key) has identical
-- semantics — rows with a NULL key remain unlimited. Dropping the predicate
-- removes the trap and keeps the guarantee.
DROP INDEX IF EXISTS uq_intercity_booking_idem;
CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_booking_idem
    ON intercity_bookings (customer_id, idempotency_key);

-- (2) intercity_corridors shipped empty, and `corridor` is NOT NULL REFERENCES
-- intercity_corridors(code) — so every publish attempt would 23503 until a row
-- existed, on a path no operator can self-serve. Seed the Phase-1 corridor.
-- Idempotent so the migration stays re-runnable.
INSERT INTO intercity_corridors (code, origin_name, destination_name)
VALUES ('KGL_MUS', 'Kigali', 'Musanze')
ON CONFLICT (code) DO NOTHING;

-- (3) cancelled_by_role was the only enum-ish column in the set without a CHECK.
-- Decision 4's cancel attribution decides whether a compensating action is owed,
-- so a typo silently mis-buckets it.
ALTER TABLE intercity_bookings
    DROP CONSTRAINT IF EXISTS cancelled_by_role_valid;
ALTER TABLE intercity_bookings
    ADD CONSTRAINT cancelled_by_role_valid
    CHECK (cancelled_by_role IS NULL
           OR cancelled_by_role IN ('CUSTOMER', 'DRIVER', 'SYSTEM', 'ADMIN'));

-- (4) one_source_only was added NOT VALID in 097 to avoid a blocking scan. It
-- stays advisory for new rows only, and unusable by the planner, until it is
-- validated. Zero existing ride_credit_ledger rows violate it, so validating is
-- free. VALIDATE takes only SHARE UPDATE EXCLUSIVE: it blocks neither reads nor
-- writes.
ALTER TABLE ride_credit_ledger VALIDATE CONSTRAINT one_source_only;

-- (5) The stale-trip sweeper scans for non-terminal trips well past departure.
-- Without this it seq-scans (verified), which becomes a periodic full-table scan
-- as trips accumulate.
CREATE INDEX IF NOT EXISTS idx_intercity_trips_stale
    ON intercity_trips (depart_at)
    WHERE deleted_at IS NULL AND status IN ('OPEN', 'BOARDING', 'IN_TRANSIT');
