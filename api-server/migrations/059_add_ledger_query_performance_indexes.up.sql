CREATE INDEX IF NOT EXISTS idx_wallet_transactions_created_at ON wallet_transactions (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payments_created_at            ON payments (created_at DESC);
