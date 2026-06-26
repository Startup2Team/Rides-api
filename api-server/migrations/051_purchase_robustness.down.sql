DROP INDEX IF EXISTS idx_purchases_expiry;
DROP INDEX IF EXISTS idx_purchases_recovery;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS expires_at;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS credits_granted;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS webhook_payload;
