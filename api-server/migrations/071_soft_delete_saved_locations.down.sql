DROP INDEX IF EXISTS idx_saved_locations_user_active;
ALTER TABLE saved_locations DROP COLUMN IF EXISTS deleted_at;
