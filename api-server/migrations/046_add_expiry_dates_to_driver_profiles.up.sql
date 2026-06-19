-- Add document expiry dates to driver_profiles
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS license_expiry_date DATE,
    ADD COLUMN IF NOT EXISTS insurance_expiry_date DATE,
    ADD COLUMN IF NOT EXISTS authorization_expiry_date DATE;
