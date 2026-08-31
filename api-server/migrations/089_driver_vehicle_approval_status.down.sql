-- Reverse of 089.
--
-- Dropping approval_status/rejection_reason discards which vehicles were
-- individually reviewed. This is safe to drop (not lossy in any way that
-- changes behaviour): before 089, activation and go-online were never gated
-- on a per-vehicle status at all, so removing the column returns the
-- platform to that exact prior behaviour. A re-apply of 089's up migration
-- re-derives approval_status from driver_profiles.approval_status at that
-- later point in time using the same backfill rule, which is the best any
-- migration can do for a column that never existed before.

BEGIN;

DROP INDEX IF EXISTS idx_driver_vehicles_approval_status;

ALTER TABLE driver_vehicles
  DROP COLUMN IF EXISTS rejection_reason,
  DROP COLUMN IF EXISTS approval_status;

COMMIT;
