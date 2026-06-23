-- 043: payment robustness on package_purchases (folded in from the team's
-- payment_requests design): raw webhook evidence, crash-recovery flag, and a
-- payment timeout.
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS webhook_payload jsonb;
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS credits_granted boolean NOT NULL DEFAULT FALSE;
ALTER TABLE package_purchases ADD COLUMN IF NOT EXISTS expires_at timestamptz;

-- Crash-recovery: a job can re-run grants for paid-but-not-granted purchases.
CREATE INDEX IF NOT EXISTS idx_purchases_recovery
    ON package_purchases (status, credits_granted)
    WHERE status = 'PAID' AND credits_granted = FALSE;

-- Expiry worker touches only still-pending purchases.
CREATE INDEX IF NOT EXISTS idx_purchases_expiry
    ON package_purchases (status, expires_at)
    WHERE status = 'PENDING';
