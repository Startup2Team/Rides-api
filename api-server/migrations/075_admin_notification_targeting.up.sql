-- Admin notification campaigns: per-vehicle-type / single-driver targeting and
-- real draft + scheduled states.
--
-- The admin console has always offered nine audiences (all, customers, all
-- drivers, one per vehicle type, one specific driver) and a "Save as draft" /
-- "Schedule for later" choice, but the table could only record the three broad
-- audiences and every row was written as SENT — so a draft or a scheduled
-- campaign was broadcast to everyone immediately.
ALTER TABLE admin_notifications
    ADD COLUMN IF NOT EXISTS target_driver_id UUID REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS scheduled_at     TIMESTAMPTZ;

-- sent_at now means "when delivery actually happened" — NULL for DRAFT and for
-- SCHEDULED campaigns that haven't fired yet.
ALTER TABLE admin_notifications ALTER COLUMN sent_at DROP NOT NULL;
ALTER TABLE admin_notifications ALTER COLUMN sent_at DROP DEFAULT;

-- Audience values grew from 'ALL' to 'DRIVER_RIFANI' / 'SINGLE_DRIVER'; leave
-- room for a longer vehicle code than the five we ship with.
ALTER TABLE admin_notifications ALTER COLUMN audience TYPE VARCHAR(40);

-- The scheduled-campaign dispatcher polls this every minute.
CREATE INDEX IF NOT EXISTS idx_admin_notifications_due
    ON admin_notifications (scheduled_at)
    WHERE status = 'SCHEDULED';
