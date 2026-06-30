-- 054: rider-submitted proof of a manual (off-platform) MoMo payment, so an
-- admin can verify it against their merchant statement and settle the purchase.
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS payment_proof_ref   varchar(120);
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS payment_proof_phone varchar(20);
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS payment_proof_url   text;
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS payment_proof_note  text;
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS payment_proof_at    timestamptz;

-- Admin queue: pending purchases that have a proof awaiting verification.
CREATE INDEX IF NOT EXISTS idx_purchases_proof_pending
    ON package_purchases (status, payment_proof_at)
    WHERE status = 'PENDING' AND payment_proof_at IS NOT NULL;
