ALTER TABLE ride_packages
    DROP COLUMN IF EXISTS cost_per_ride_rwf,
    DROP COLUMN IF EXISTS updated_at;

DELETE FROM platform_settings WHERE key = 'payment';
