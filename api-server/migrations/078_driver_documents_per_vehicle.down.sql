-- Reverse of 078.
--
-- LOSSY, deliberately. Dropping vehicle_id discards which vehicle each insurance
-- and authorization document belonged to, and the old single-index rule allows
-- only one live row per (driver_id, document_type) — so a driver with paperwork
-- for two vehicles has more live rows than the restored index permits.
--
-- The extra rows are superseded rather than deleted: the images stay in R2 and
-- the review history stays readable, which matters because these are KYC
-- records. Re-applying 078 will NOT bring them back as live documents; they must
-- be re-uploaded per vehicle.

BEGIN;

ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS driver_documents_type_scope_chk;

DROP INDEX IF EXISTS idx_driver_documents_vehicle_live_lookup;
DROP INDEX IF EXISTS idx_driver_documents_vehicle_live;
DROP INDEX IF EXISTS idx_driver_documents_person_live;

-- Keep the newest live row per (driver_id, document_type); supersede the rest so
-- the restored unique index can be created.
WITH ranked AS (
  SELECT id,
         row_number() OVER (
           PARTITION BY driver_id, document_type
           ORDER BY uploaded_at DESC, id
         ) AS rn
  FROM driver_documents
  WHERE superseded_at IS NULL
)
UPDATE driver_documents d
SET superseded_at = NOW()
FROM ranked
WHERE d.id = ranked.id AND ranked.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_documents_driver_type_live
  ON driver_documents (driver_id, document_type)
  WHERE superseded_at IS NULL;

ALTER TABLE driver_documents DROP COLUMN IF EXISTS vehicle_id;

COMMIT;
