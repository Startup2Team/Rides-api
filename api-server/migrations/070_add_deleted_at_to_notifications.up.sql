-- Soft delete for notifications: keep the row, hide it. Users often want back
-- what they "deleted", and we keep the record for audit/analytics.
ALTER TABLE notifications ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_notifications_user_active
  ON notifications (user_id, sent_at DESC)
  WHERE deleted_at IS NULL;
