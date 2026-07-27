DROP INDEX IF EXISTS idx_admin_notifications_due;

ALTER TABLE admin_notifications ALTER COLUMN audience TYPE VARCHAR(20);

-- Restore the NOT NULL contract: rows that never sent get their creation time.
UPDATE admin_notifications SET sent_at = created_at WHERE sent_at IS NULL;
ALTER TABLE admin_notifications ALTER COLUMN sent_at SET DEFAULT NOW();
ALTER TABLE admin_notifications ALTER COLUMN sent_at SET NOT NULL;

ALTER TABLE admin_notifications
    DROP COLUMN IF EXISTS scheduled_at,
    DROP COLUMN IF EXISTS target_driver_id;
