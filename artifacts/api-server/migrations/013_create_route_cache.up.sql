CREATE TABLE IF NOT EXISTS route_cache (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cache_key        TEXT NOT NULL UNIQUE,
    origin_geohash   TEXT NOT NULL,
    dest_geohash     TEXT NOT NULL,
    vehicle_type     TEXT NOT NULL,
    distance_km      DOUBLE PRECISION NOT NULL,
    duration_minutes INT NOT NULL,
    use_count        INT NOT NULL DEFAULT 1,
    agreed_fares     JSONB NOT NULL DEFAULT '[]',
    avg_fare_rwf     INT,
    last_used_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_route_cache_key      ON route_cache(cache_key);
CREATE INDEX IF NOT EXISTS idx_route_cache_origin   ON route_cache(origin_geohash);
CREATE INDEX IF NOT EXISTS idx_route_cache_dest     ON route_cache(dest_geohash);
CREATE INDEX IF NOT EXISTS idx_route_cache_use      ON route_cache(use_count DESC);
