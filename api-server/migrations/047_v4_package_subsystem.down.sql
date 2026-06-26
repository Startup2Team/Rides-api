-- 039 down: drop the v4 package subsystem (expand step is fully additive).
-- NOTE: admin_audit_log is owned by migration 034 — NOT dropped here.
DROP TABLE IF EXISTS driver_entitlements;
DROP TABLE IF EXISTS ride_credit_ledger;
DROP TABLE IF EXISTS package_purchases;
DROP TABLE IF EXISTS campaigns;
DROP TABLE IF EXISTS ride_package_versions;
ALTER TABLE ride_packages DROP COLUMN IF EXISTS code;
