-- Merchant code (optional alternative to MoMo phone) for driver payouts.
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS merchant_pay_code VARCHAR(100);
