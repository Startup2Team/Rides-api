-- Admin JWT user_id is admin_accounts.id, not users.id.
ALTER TABLE driver_profiles
    DROP CONSTRAINT IF EXISTS driver_profiles_approved_by_fkey;

ALTER TABLE driver_profiles
    ADD CONSTRAINT driver_profiles_approved_by_fkey
        FOREIGN KEY (approved_by) REFERENCES admin_accounts(id);
