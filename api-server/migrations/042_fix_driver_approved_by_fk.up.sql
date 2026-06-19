-- driver_profiles.approved_by records the ADMIN who approved the driver.
-- Admins live in admin_accounts, not users, so the original FK to users(id)
-- made approval impossible (FK violation: admin id not present in users).
-- Re-point the FK to admin_accounts.
ALTER TABLE driver_profiles DROP CONSTRAINT IF EXISTS driver_profiles_approved_by_fkey;
ALTER TABLE driver_profiles
  ADD CONSTRAINT driver_profiles_approved_by_fkey
  FOREIGN KEY (approved_by) REFERENCES admin_accounts(id) ON DELETE SET NULL;
