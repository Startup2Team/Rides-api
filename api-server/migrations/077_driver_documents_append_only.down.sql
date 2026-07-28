-- Reverting is LOSSY: the original unique index covers every row, so the
-- version history has to go before it can be recreated. Superseded rows are
-- deleted; only the live version of each document survives.
DELETE FROM driver_documents WHERE superseded_at IS NOT NULL;

DROP INDEX IF EXISTS idx_driver_documents_live_review;
DROP INDEX IF EXISTS idx_driver_documents_driver_type_live;

-- Recreate the pre-077 index. Safe now that duplicates are gone.
CREATE UNIQUE INDEX idx_driver_documents_driver_type
  ON driver_documents (driver_id, document_type);

ALTER TABLE driver_documents
  DROP CONSTRAINT IF EXISTS driver_documents_review_status_chk;

ALTER TABLE driver_documents
  DROP COLUMN IF EXISTS reupload_requested_by,
  DROP COLUMN IF EXISTS reupload_requested_at,
  DROP COLUMN IF EXISTS review_notes,
  DROP COLUMN IF EXISTS reviewed_by,
  DROP COLUMN IF EXISTS reviewed_at,
  DROP COLUMN IF EXISTS review_status,
  DROP COLUMN IF EXISTS sha256,
  DROP COLUMN IF EXISTS superseded_at;
