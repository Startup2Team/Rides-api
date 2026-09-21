-- Reverse of 100.
-- The seeded vehicle types are deliberately NOT deleted: driver_vehicles rows
-- may reference them, and removing a referenced type raises a foreign-key
-- violation that aborts this migration and leaves schema_migrations DIRTY,
-- which stops the API booting. An unused catalogue row is harmless.
-- Restoring the 1..4 cap would fail if any booking larger than 4 seats exists,
-- so widen-only on the way down: leave the structural bound in place.
DROP INDEX IF EXISTS idx_intercity_trips_operator;
ALTER TABLE intercity_trips DROP COLUMN IF EXISTS operator_id;
DROP TABLE IF EXISTS intercity_operator_drivers;
DROP TABLE IF EXISTS intercity_operators;
