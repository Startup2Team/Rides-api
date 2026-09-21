-- Reverse of 099.
-- NOTE: restoring the PARTIAL idempotency index also restores the ON CONFLICT
-- inference trap — any caller using the bare `ON CONFLICT (customer_id,
-- idempotency_key)` form will fail at plan time once this runs.
DROP INDEX IF EXISTS idx_intercity_trips_stale;
ALTER TABLE ride_credit_ledger DROP CONSTRAINT IF EXISTS one_source_only;
ALTER TABLE ride_credit_ledger
    ADD CONSTRAINT one_source_only
    CHECK (num_nonnulls(source_ride_id, source_purchase_id, source_intercity_trip_id) <= 1)
    NOT VALID;
ALTER TABLE intercity_bookings DROP CONSTRAINT IF EXISTS cancelled_by_role_valid;

-- The seeded corridor is deliberately NOT deleted. It is reference data, and
-- trips carry `corridor NOT NULL REFERENCES intercity_corridors(code)`, so
-- removing it raises a foreign-key violation the moment any trip exists — which
-- aborts this migration and leaves schema_migrations DIRTY, meaning the API
-- refuses to boot. Leaving an unused lookup row behind is harmless; wedging the
-- deploy is not.
DROP INDEX IF EXISTS uq_intercity_booking_idem;
CREATE UNIQUE INDEX IF NOT EXISTS uq_intercity_booking_idem
    ON intercity_bookings (customer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
