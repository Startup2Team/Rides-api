-- Make driver_documents append-only, hashed, and individually reviewable.
--
-- Until now both upload paths (driver self-service and admin-on-behalf) did
-- `INSERT ... ON CONFLICT (driver_id, document_type) DO UPDATE SET file_url`,
-- so a re-upload OVERWROTE the row in place. Three consequences:
--
--   1. No history. "What did we approve on the 4th?" was unanswerable — the
--      approved file_url was simply gone.
--   2. No approval gate. An APPROVED driver could swap a licence for anything
--      and stay APPROVED, because approval lives on driver_profiles and nothing
--      linked it to a specific file.
--   3. The decision never recorded WHICH bytes were approved.
--
-- This migration adds the version chain, the content hash, and per-document
-- review state so those become answerable. The append-only write itself is
-- enforced in the repository layer; this makes it representable.

ALTER TABLE driver_documents
  -- NULL = this is the live version. Non-NULL = superseded by a later upload.
  ADD COLUMN superseded_at TIMESTAMPTZ,
  -- SHA-256 of the stored bytes. Nullable: rows predating this migration have
  -- no hash, and presigned uploads may record it after the fact.
  ADD COLUMN sha256 CHAR(64),
  -- Per-document review state, independent of the driver's overall approval.
  ADD COLUMN review_status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
  ADD COLUMN reviewed_at TIMESTAMPTZ,
  ADD COLUMN reviewed_by UUID,
  ADD COLUMN review_notes TEXT,
  -- An open, admin-initiated window permitting ONE replacement of an approved
  -- document. Without this, an approved document is view-only server-side.
  ADD COLUMN reupload_requested_at TIMESTAMPTZ,
  ADD COLUMN reupload_requested_by UUID;

ALTER TABLE driver_documents
  ADD CONSTRAINT driver_documents_review_status_chk
  CHECK (review_status IN ('PENDING', 'APPROVED', 'REJECTED'));

-- The old unique index covered ALL rows, which is precisely what forced the
-- overwrite. Constrain only the live version so history can accumulate.
DROP INDEX IF EXISTS idx_driver_documents_driver_type;
CREATE UNIQUE INDEX idx_driver_documents_driver_type_live
  ON driver_documents (driver_id, document_type)
  WHERE superseded_at IS NULL;

-- Admin review queues read live rows by state.
CREATE INDEX idx_driver_documents_live_review
  ON driver_documents (driver_id, review_status)
  WHERE superseded_at IS NULL;

-- Backfill: a document belonging to an already-APPROVED driver was, in effect,
-- approved. Without this every existing approved driver would suddenly show
-- PENDING documents and be dragged back into the review queue on deploy.
UPDATE driver_documents d
   SET review_status = 'APPROVED',
       reviewed_at   = dp.approved_at
  FROM driver_profiles dp
 WHERE dp.id = d.driver_id
   AND dp.approval_status = 'APPROVED'
   AND d.superseded_at IS NULL;
