DROP INDEX IF EXISTS idx_purchases_proof_pending;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS payment_proof_at;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS payment_proof_note;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS payment_proof_url;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS payment_proof_phone;
ALTER TABLE package_purchases DROP COLUMN IF EXISTS payment_proof_ref;
