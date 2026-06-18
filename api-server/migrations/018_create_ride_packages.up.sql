CREATE TABLE ride_packages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(50) NOT NULL,
    vehicle_type_id UUID NOT NULL REFERENCES vehicle_types(id),
    ride_count      INTEGER NOT NULL,
    validity_days   INTEGER NOT NULL DEFAULT 30,
    price_rwf       INTEGER NOT NULL,
    is_promotional  BOOLEAN NOT NULL DEFAULT FALSE,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
