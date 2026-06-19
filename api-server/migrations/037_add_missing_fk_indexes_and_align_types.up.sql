-- 037: Add missing FK indexes + align column types
-- All indexes are safe on a live DB (no lock, no rewrite).
-- Type changes affect small/empty tables only.

-- ── Missing FK indexes ──────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_admin_accounts_role ON admin_accounts (role_id);
CREATE INDEX IF NOT EXISTS idx_bonus_grants_vehicle_type ON bonus_grants (vehicle_type_id);
CREATE INDEX IF NOT EXISTS idx_bonus_tiers_vehicle_type ON bonus_tiers (vehicle_type_id);
CREATE INDEX IF NOT EXISTS idx_driver_profiles_approved_by ON driver_profiles (approved_by) WHERE approved_by IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_driver_credits_package ON driver_ride_credits (package_id);
CREATE INDEX IF NOT EXISTS idx_driver_credits_vehicle_type ON driver_ride_credits (vehicle_type_id);
CREATE INDEX IF NOT EXISTS idx_driver_sessions_vehicle ON driver_sessions (vehicle_id);
CREATE INDEX IF NOT EXISTS idx_ride_disputes_raised_by ON ride_disputes (raised_by_id);
CREATE INDEX IF NOT EXISTS idx_ride_disputes_resolved_by ON ride_disputes (resolved_by) WHERE resolved_by IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ride_packages_vehicle_type ON ride_packages (vehicle_type_id);
CREATE INDEX IF NOT EXISTS idx_rides_pricing_config ON rides (pricing_config_id) WHERE pricing_config_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_safety_incidents_ride ON safety_incidents (ride_id) WHERE ride_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_safety_incidents_reporter ON safety_incidents (reporter_user_id) WHERE reporter_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_support_tickets_ride ON support_tickets (ride_id) WHERE ride_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_support_tickets_user ON support_tickets (from_user_id) WHERE from_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_vpc_created_by ON vehicle_pricing_configs (created_by) WHERE created_by IS NOT NULL;

-- ── Type alignments ─────────────────────────────────────────────────────────

-- payments.amount_rwf: integer → bigint (matches wallet_transactions)
ALTER TABLE payments ALTER COLUMN amount_rwf TYPE bigint;
ALTER TABLE payments ALTER COLUMN platform_fee_rwf TYPE bigint;
ALTER TABLE payments ALTER COLUMN driver_amount_rwf TYPE bigint;

-- driver_profiles.load_capacity_kg: integer → numeric (matches driver_vehicles)
ALTER TABLE driver_profiles ALTER COLUMN load_capacity_kg TYPE numeric;

-- landmarks: text columns → varchar for consistency with hot_zones
ALTER TABLE landmarks ALTER COLUMN category TYPE character varying;
ALTER TABLE landmarks ALTER COLUMN geohash6 TYPE character varying;
ALTER TABLE landmarks ALTER COLUMN name TYPE character varying;

-- safety_incidents.district: text → varchar (matches driver_profiles)
ALTER TABLE safety_incidents ALTER COLUMN district TYPE character varying;
