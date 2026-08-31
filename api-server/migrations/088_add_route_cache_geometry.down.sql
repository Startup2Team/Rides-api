-- Reverse 088. Non-lossy for pre-OSRM data (both columns were NULL on every
-- row that predates this migration); any OSRM-sourced geometry/duration
-- captured while it was live is dropped, which is the intended meaning of
-- rolling this back.
ALTER TABLE route_cache DROP COLUMN IF EXISTS duration_seconds;
ALTER TABLE route_cache DROP COLUMN IF EXISTS geometry;
