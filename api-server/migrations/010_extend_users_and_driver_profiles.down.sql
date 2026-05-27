ALTER TABLE users DROP COLUMN IF EXISTS email;

ALTER TABLE driver_profiles
    DROP COLUMN IF EXISTS province,
    DROP COLUMN IF EXISTS district,
    DROP COLUMN IF EXISTS sector,
    DROP COLUMN IF EXISTS cell,
    DROP COLUMN IF EXISTS village,
    DROP COLUMN IF EXISTS passenger_seats,
    DROP COLUMN IF EXISTS load_capacity_kg,
    DROP COLUMN IF EXISTS momo_provider,
    DROP COLUMN IF EXISTS policy_accepted,
    DROP COLUMN IF EXISTS policy_accepted_at;
