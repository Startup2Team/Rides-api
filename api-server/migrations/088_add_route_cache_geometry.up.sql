-- OSRM real-road routing (feat/osrm-routing): additive, nullable columns so
-- the existing Haversine-only rows and code paths are unaffected.
--
-- geometry         — encoded polyline (OSRM default precision-5) of the
--                     route path, passed through to the client to draw.
-- duration_seconds — OSRM's raw duration, kept alongside the existing
--                     duration_minutes (rounded) for a precise ETA.
--
-- Both NULL for every row written before this migration and for any row
-- written while OSRM_URL is unset — the Haversine estimate path never
-- populates them.
ALTER TABLE route_cache ADD COLUMN IF NOT EXISTS geometry TEXT;
ALTER TABLE route_cache ADD COLUMN IF NOT EXISTS duration_seconds INT;
