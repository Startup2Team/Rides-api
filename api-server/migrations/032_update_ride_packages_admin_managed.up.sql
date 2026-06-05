-- Remove the seeded packages so admin controls them entirely.
-- Admin will create packages via the API; nothing is hardcoded.
DELETE FROM ride_packages;

-- Add per-package cost_per_ride_rwf so each package can have independent pricing.
-- Default 30 RWF/ride (moto baseline); admin sets it when creating.
ALTER TABLE ride_packages
    ADD COLUMN IF NOT EXISTS cost_per_ride_rwf INTEGER NOT NULL DEFAULT 30,
    ADD COLUMN IF NOT EXISTS updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Also store platform MoMo payment number in platform_settings.
INSERT INTO platform_settings (key, value) VALUES
    ('payment', '{"mtn_momo_number": "", "airtel_number": "", "instructions": "Pay to the number shown, then submit your transaction ID."}')
ON CONFLICT (key) DO NOTHING;
