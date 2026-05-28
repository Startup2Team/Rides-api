-- Track whether a driver has used their one-time free trial
ALTER TABLE driver_profiles ADD COLUMN IF NOT EXISTS free_trial_used BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE driver_ride_credits (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id        UUID NOT NULL REFERENCES users(id),
    package_id       UUID NOT NULL REFERENCES ride_packages(id),
    vehicle_type_id  UUID NOT NULL REFERENCES vehicle_types(id),
    rides_total      INTEGER NOT NULL,
    rides_remaining  INTEGER NOT NULL,
    is_promotional   BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at       TIMESTAMPTZ NOT NULL,
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    purchased_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT rides_remaining_non_negative CHECK (rides_remaining >= 0)
);

CREATE INDEX idx_driver_credits_active ON driver_ride_credits (driver_id, is_active, expires_at)
    WHERE is_active = TRUE;
