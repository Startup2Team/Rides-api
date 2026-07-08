DROP INDEX IF EXISTS idx_driver_profiles_referred_by;
ALTER TABLE driver_profiles DROP COLUMN IF EXISTS referred_by_driver_id;
