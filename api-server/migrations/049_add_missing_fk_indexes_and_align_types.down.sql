-- 037 down: Remove FK indexes and revert type changes

DROP INDEX IF EXISTS idx_admin_accounts_role;
DROP INDEX IF EXISTS idx_bonus_grants_vehicle_type;
DROP INDEX IF EXISTS idx_bonus_tiers_vehicle_type;
DROP INDEX IF EXISTS idx_driver_profiles_approved_by;
DROP INDEX IF EXISTS idx_driver_credits_package;
DROP INDEX IF EXISTS idx_driver_credits_vehicle_type;
DROP INDEX IF EXISTS idx_driver_sessions_vehicle;
DROP INDEX IF EXISTS idx_ride_disputes_raised_by;
DROP INDEX IF EXISTS idx_ride_disputes_resolved_by;
DROP INDEX IF EXISTS idx_ride_packages_vehicle_type;
DROP INDEX IF EXISTS idx_rides_pricing_config;
DROP INDEX IF EXISTS idx_safety_incidents_ride;
DROP INDEX IF EXISTS idx_safety_incidents_reporter;
DROP INDEX IF EXISTS idx_support_tickets_ride;
DROP INDEX IF EXISTS idx_support_tickets_user;
DROP INDEX IF EXISTS idx_vpc_created_by;

ALTER TABLE payments ALTER COLUMN amount_rwf TYPE integer;
ALTER TABLE payments ALTER COLUMN platform_fee_rwf TYPE integer;
ALTER TABLE payments ALTER COLUMN driver_amount_rwf TYPE integer;

ALTER TABLE driver_profiles ALTER COLUMN load_capacity_kg TYPE integer;

ALTER TABLE landmarks ALTER COLUMN category TYPE text;
ALTER TABLE landmarks ALTER COLUMN geohash6 TYPE text;
ALTER TABLE landmarks ALTER COLUMN name TYPE text;

ALTER TABLE safety_incidents ALTER COLUMN district TYPE text;
