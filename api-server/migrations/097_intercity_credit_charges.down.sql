-- Reverse of 097.
-- WARNING: dropping source_intercity_trip_id permanently destroys the audit
-- linkage between ride_credit_ledger rows and the intercity trips that caused
-- them. The ledger rows themselves survive, but "what did this trip cost?"
-- becomes unanswerable.
DROP TABLE IF EXISTS intercity_credit_charges;
DROP INDEX IF EXISTS idx_rcl_intercity;
ALTER TABLE ride_credit_ledger DROP CONSTRAINT IF EXISTS one_source_only;
ALTER TABLE ride_credit_ledger DROP COLUMN IF EXISTS source_intercity_trip_id;
