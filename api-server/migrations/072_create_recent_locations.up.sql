-- Google-Maps-style "recent places" for customers. Distinct from saved_locations
-- (home/work). Written when a rider picks a place for a booking; re-picking the
-- same address bumps last_used_at + use_count. Soft-deletable (deleted_at).
CREATE TABLE IF NOT EXISTS recent_locations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    address      TEXT NOT NULL,
    lat          DOUBLE PRECISION NOT NULL,
    lng          DOUBLE PRECISION NOT NULL,
    use_count    INTEGER NOT NULL DEFAULT 1,
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ
);
-- One LIVE row per (user, address) so re-picking upserts instead of duplicating.
CREATE UNIQUE INDEX IF NOT EXISTS uq_recent_locations_user_addr_active
    ON recent_locations (user_id, address) WHERE deleted_at IS NULL;
-- Hot path: most-recent-first for a user.
CREATE INDEX IF NOT EXISTS idx_recent_locations_user_recent
    ON recent_locations (user_id, last_used_at DESC) WHERE deleted_at IS NULL;
