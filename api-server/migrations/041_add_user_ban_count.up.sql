-- Lifetime count of temporary cancellation bans a user has received. When it
-- reaches the configured limit, the next ban becomes a full (indefinite)
-- suspension. Applies to both customers and drivers (same users row).
ALTER TABLE users ADD COLUMN IF NOT EXISTS ban_count INTEGER NOT NULL DEFAULT 0;
