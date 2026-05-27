CREATE TABLE IF NOT EXISTS driver_locations (
    driver_id          UUID PRIMARY KEY REFERENCES driver_profiles(id) ON DELETE CASCADE,
    location           GEOGRAPHY(POINT, 4326) NOT NULL,
    speed_kmh          DECIMAL(6,2),
    heading            DECIMAL(5,2),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_driver_locations_geo ON driver_locations USING GIST(location);
