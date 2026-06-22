-- Drop document expiry dates from driver_profiles
ALTER TABLE driver_profiles
    DROP COLUMN IF EXISTS license_expiry_date,
    DROP COLUMN IF EXISTS insurance_expiry_date,
    DROP COLUMN IF EXISTS authorization_expiry_date;
