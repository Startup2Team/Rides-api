DROP VIEW  IF EXISTS driver_purchase_counts;
DROP INDEX IF EXISTS uniq_bonus_registration;
DROP TABLE IF EXISTS bonus_grants;
DROP TABLE IF EXISTS bonus_tiers;
ALTER TABLE driver_profiles DROP COLUMN IF EXISTS registration_bonus_granted;
