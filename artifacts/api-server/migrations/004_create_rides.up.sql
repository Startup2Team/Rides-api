CREATE TABLE IF NOT EXISTS rides (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           UUID NOT NULL REFERENCES users(id),
    driver_id             UUID REFERENCES driver_profiles(id),
    transport_type        VARCHAR(20) NOT NULL,
    status                VARCHAR(30) NOT NULL DEFAULT 'SEARCHING',
    pickup_point          GEOGRAPHY(POINT, 4326) NOT NULL,
    pickup_address        TEXT NOT NULL,
    destination_point     GEOGRAPHY(POINT, 4326) NOT NULL,
    destination_address   TEXT NOT NULL,
    estimated_distance_km DECIMAL(8,3),
    customer_initial_fare DECIMAL(10,2),
    agreed_fare           DECIMAL(10,2),
    fare_locked_at        TIMESTAMPTZ,
    cancel_reason         TEXT,
    cancelled_by_role     VARCHAR(20),
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rides_customer_id ON rides(customer_id);
CREATE INDEX IF NOT EXISTS idx_rides_driver_id ON rides(driver_id);
CREATE INDEX IF NOT EXISTS idx_rides_status ON rides(status);
CREATE INDEX IF NOT EXISTS idx_rides_created_at ON rides(created_at DESC);
