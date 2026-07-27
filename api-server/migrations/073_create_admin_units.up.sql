-- Rwanda administrative hierarchy (self-referencing): province → district →
-- sector → cell → village. Seeded once at boot from the embedded official
-- dataset (see internal/location/admin_units_seed.go). Enables structured
-- address pickers/autocomplete WITHOUT calling Google/Mapbox, normalized
-- pickup/dropoff, and coarse admin-unit matching/heatmaps.
CREATE TABLE IF NOT EXISTS admin_units (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id  UUID REFERENCES admin_units(id) ON DELETE CASCADE,
    level      TEXT NOT NULL CHECK (level IN ('province','district','sector','cell','village')),
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_admin_units_parent ON admin_units(parent_id);
CREATE INDEX IF NOT EXISTS idx_admin_units_level ON admin_units(level);
CREATE INDEX IF NOT EXISTS idx_admin_units_name_lower ON admin_units(lower(name));
