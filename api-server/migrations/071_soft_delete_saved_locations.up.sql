ALTER TABLE saved_locations ADD COLUMN deleted_at TIMESTAMPTZ;
-- Hot path (list by user) only ever wants live rows.
CREATE INDEX IF NOT EXISTS idx_saved_locations_user_active
  ON saved_locations (user_id) WHERE deleted_at IS NULL;
