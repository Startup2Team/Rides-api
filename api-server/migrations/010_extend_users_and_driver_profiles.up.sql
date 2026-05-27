-- Add email to users
ALTER TABLE users ADD COLUMN IF NOT EXISTS email VARCHAR(255);

-- Add Rwanda admin cascade + vehicle capacity + momo provider to driver_profiles
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS province         VARCHAR(100),
    ADD COLUMN IF NOT EXISTS district         VARCHAR(100),
    ADD COLUMN IF NOT EXISTS sector           VARCHAR(100),
    ADD COLUMN IF NOT EXISTS cell             VARCHAR(100),
    ADD COLUMN IF NOT EXISTS village          VARCHAR(100),
    ADD COLUMN IF NOT EXISTS passenger_seats  INT,
    ADD COLUMN IF NOT EXISTS load_capacity_kg INT,
    ADD COLUMN IF NOT EXISTS momo_provider    VARCHAR(20),
    ADD COLUMN IF NOT EXISTS policy_accepted     BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS policy_accepted_at  TIMESTAMPTZ;
