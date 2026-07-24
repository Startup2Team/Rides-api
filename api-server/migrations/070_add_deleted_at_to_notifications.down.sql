DROP INDEX IF EXISTS idx_notifications_user_active;
ALTER TABLE notifications DROP COLUMN IF EXISTS deleted_at;
