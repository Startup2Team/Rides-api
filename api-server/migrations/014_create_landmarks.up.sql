CREATE TABLE IF NOT EXISTS landmarks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    category   TEXT NOT NULL,
    lat        DOUBLE PRECISION NOT NULL,
    lng        DOUBLE PRECISION NOT NULL,
    geohash6   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_landmarks_geohash  ON landmarks(geohash6);
CREATE INDEX IF NOT EXISTS idx_landmarks_category ON landmarks(category);
