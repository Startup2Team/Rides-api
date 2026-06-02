-- Rollback migration 029

DROP TRIGGER IF EXISTS trg_create_customer_profile ON users;
DROP FUNCTION IF EXISTS create_customer_profile();

DROP TABLE IF EXISTS driver_sessions CASCADE;
DROP TABLE IF EXISTS notifications CASCADE;
DROP TABLE IF EXISTS ride_disputes CASCADE;
DROP TABLE IF EXISTS ratings CASCADE;
DROP TABLE IF EXISTS wallet_transactions CASCADE;
DROP TABLE IF EXISTS wallets CASCADE;
DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS zone_demand_stats CASCADE;
DROP TABLE IF EXISTS hot_zones CASCADE;
DROP TABLE IF EXISTS driver_vehicles CASCADE;
DROP TABLE IF EXISTS customer_profiles CASCADE;

ALTER TABLE driver_profiles
    DROP COLUMN IF EXISTS rating,
    DROP COLUMN IF EXISTS total_earnings_rwf,
    DROP COLUMN IF EXISTS cancellation_rate,
    DROP COLUMN IF EXISTS daily_declines,
    DROP COLUMN IF EXISTS suspicious_disconnect_count;

ALTER TABLE users
    DROP COLUMN IF EXISTS profile_image_url,
    DROP COLUMN IF EXISTS current_mode,
    DROP COLUMN IF EXISTS suspension_reason,
    DROP COLUMN IF EXISTS last_seen_at;
