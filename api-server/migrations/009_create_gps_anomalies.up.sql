CREATE TABLE IF NOT EXISTS gps_anomalies (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id      UUID NOT NULL REFERENCES driver_profiles(id),
    computed_speed DECIMAL(8,2) NOT NULL,
    last_location  GEOGRAPHY(POINT, 4326),
    new_location   GEOGRAPHY(POINT, 4326),
    detected_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_gps_anomalies_driver_id ON gps_anomalies(driver_id);
CREATE INDEX IF NOT EXISTS idx_gps_anomalies_detected_at ON gps_anomalies(detected_at DESC);

-- Analytics events — written by business logic, consumed by background reader
CREATE TABLE IF NOT EXISTS analytics_events (
    id           BIGSERIAL PRIMARY KEY,
    event_type   VARCHAR(100) NOT NULL,
    actor_role   VARCHAR(20),
    actor_id     UUID,
    ride_id      UUID,
    payload      JSONB NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_analytics_events_event_type ON analytics_events(event_type);
CREATE INDEX IF NOT EXISTS idx_analytics_events_occurred_at ON analytics_events(occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_analytics_events_ride_id ON analytics_events(ride_id);
