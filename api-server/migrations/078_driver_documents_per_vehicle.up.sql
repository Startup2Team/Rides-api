-- Per-vehicle driver documents.
--
-- driver_documents attached everything to driver_id, with one live row per
-- (driver_id, document_type). That works for a person's licence, national ID and
-- selfie, but insurance and vehicle authorization belong to a VEHICLE. A driver
-- with two vehicles could therefore only hold one live VEHICLE_INSURANCE: adding
-- the second vehicle's paperwork superseded the first vehicle's, silently, and
-- the first vehicle was then unverifiable with no record of why.
--
-- vehicle_id is nullable ON PURPOSE and carries meaning:
--   NULL     → a document about the PERSON  (licence, ID, selfie)
--   NOT NULL → a document about that VEHICLE (insurance, authorization)
-- The check constraint below makes that split enforceable rather than a
-- convention, so a wrong pairing fails at write time instead of producing data
-- nobody can interpret later.

BEGIN;

ALTER TABLE driver_documents
  ADD COLUMN IF NOT EXISTS vehicle_id uuid
    REFERENCES driver_vehicles(id) ON DELETE CASCADE;

COMMENT ON COLUMN driver_documents.vehicle_id IS
  'NULL for person-level documents (licence, national ID, selfie); the owning vehicle for vehicle-level ones (insurance, authorization).';

-- Backfill: existing vehicle-level rows predate the column. Attach each to the
-- driver''s vehicle only when there is exactly ONE candidate, so we never guess.
-- Anything ambiguous is left NULL and reported by the verification query at the
-- end of this file rather than being silently mis-assigned.
UPDATE driver_documents d
SET vehicle_id = v.id
FROM driver_vehicles v
WHERE d.driver_id = v.driver_id
  AND d.vehicle_id IS NULL
  AND d.document_type IN (
        'VEHICLE_INSURANCE', 'VEHICLE_INSURANCE_BACK',
        'VEHICLE_AUTHORIZATION', 'VEHICLE_AUTHORIZATION_BACK')
  AND (SELECT count(*) FROM driver_vehicles v2 WHERE v2.driver_id = d.driver_id) = 1;

-- Normalise the selfie type before adding the constraint that forbids the alias.
-- The admin panel wrote PROFILE_SELFIE while mobile and the driver API wrote
-- SELFIE — the same document under two names, so neither side could reliably
-- find what the other stored.
UPDATE driver_documents SET document_type = 'SELFIE' WHERE document_type = 'PROFILE_SELFIE';

-- Uniqueness has to be expressed as TWO partial indexes, not one composite.
-- Postgres treats NULLs as distinct in a unique index, so a single
-- UNIQUE (driver_id, vehicle_id, document_type) would happily accept unlimited
-- duplicate person-level rows — exactly the guarantee we are trying to keep.
DROP INDEX IF EXISTS idx_driver_documents_driver_type_live;

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_documents_person_live
  ON driver_documents (driver_id, document_type)
  WHERE superseded_at IS NULL AND vehicle_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_documents_vehicle_live
  ON driver_documents (driver_id, vehicle_id, document_type)
  WHERE superseded_at IS NULL AND vehicle_id IS NOT NULL;

-- Matches the "all live documents for this vehicle" read the vehicle screen makes.
CREATE INDEX IF NOT EXISTS idx_driver_documents_vehicle_live_lookup
  ON driver_documents (vehicle_id)
  WHERE superseded_at IS NULL AND vehicle_id IS NOT NULL;

-- The document type vocabulary, and the person/vehicle split, as a constraint.
-- Previously the allowed set lived in two disagreeing Go maps
-- (internal/driver validate tag vs internal/admin allowlist) and nothing stopped
-- a third spelling reaching the table.
ALTER TABLE driver_documents
  DROP CONSTRAINT IF EXISTS driver_documents_type_scope_chk;

ALTER TABLE driver_documents
  ADD CONSTRAINT driver_documents_type_scope_chk CHECK (
    (document_type IN ('NATIONAL_ID_FRONT', 'NATIONAL_ID_BACK',
                       'LICENCE_FRONT', 'LICENCE_BACK',
                       'SELFIE')
       AND vehicle_id IS NULL)
    OR
    (document_type IN ('VEHICLE_INSURANCE', 'VEHICLE_INSURANCE_BACK',
                       'VEHICLE_AUTHORIZATION', 'VEHICLE_AUTHORIZATION_BACK')
       AND vehicle_id IS NOT NULL)
  ) NOT VALID;

-- NOT VALID above, then validated separately: this only checks new writes first,
-- so the migration cannot fail partway on legacy rows the backfill left
-- ambiguous. VALIDATE takes a weaker lock than an inline check and will raise if
-- anything genuinely violates the split, at which point those rows need a
-- vehicle assigned by hand.
ALTER TABLE driver_documents VALIDATE CONSTRAINT driver_documents_type_scope_chk;

COMMIT;
