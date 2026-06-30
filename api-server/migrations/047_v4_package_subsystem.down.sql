-- 039 down: drop the v4 package subsystem (expand step is fully additive).
DROP TABLE IF EXISTS admin_audit_log;
DROP TABLE IF EXISTS driver_entitlements;
DROP TABLE IF EXISTS ride_credit_ledger;
DROP TABLE IF EXISTS package_purchases;
DROP TABLE IF EXISTS campaigns;
DROP TABLE IF EXISTS ride_package_versions;
ALTER TABLE ride_packages DROP COLUMN IF EXISTS code;
