DROP INDEX IF EXISTS idx_notif_user_time;
DROP TABLE IF EXISTS negotiation_messages;
ALTER TABLE negotiation_rounds DROP COLUMN IF EXISTS message;
