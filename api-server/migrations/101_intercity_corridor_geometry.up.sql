-- =====================================================
-- Migration 101: Intercity — corridor endpoint coordinates
--
-- A corridor IS a fixed pair of places. Requiring the CLIENT to supply
-- origin_name, destination_name and four coordinates at publish meant every
-- caller had to invent geography for a route the server already knows, and
-- publish could not succeed from the app at all: intercity_corridors carried no
-- coordinates while intercity_trips.origin_point/destination_point are NOT NULL.
--
-- Two drivers typing "Musanze" would also have pinned it in two different
-- places — the same fragmentation the corridor lookup table exists to prevent,
-- reappearing one column over.
-- =====================================================

ALTER TABLE intercity_corridors
    ADD COLUMN IF NOT EXISTS origin_point      GEOGRAPHY(POINT, 4326),
    ADD COLUMN IF NOT EXISTS destination_point GEOGRAPHY(POINT, 4326);

-- Phase-1 corridor. Kigali = Nyabugogo taxi park; Musanze = town centre.
UPDATE intercity_corridors
   SET origin_point      = ST_SetSRID(ST_MakePoint(30.0588, -1.9302), 4326)::geography,
       destination_point = ST_SetSRID(ST_MakePoint(29.6344, -1.4995), 4326)::geography
 WHERE code = 'KGL_MUS'
   AND (origin_point IS NULL OR destination_point IS NULL);

-- Enforced only once every row has been given coordinates, so the migration
-- cannot fail on a corridor someone added between 099 and here.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM intercity_corridors
         WHERE origin_point IS NULL OR destination_point IS NULL
    ) THEN
        ALTER TABLE intercity_corridors
            ALTER COLUMN origin_point SET NOT NULL,
            ALTER COLUMN destination_point SET NOT NULL;
    ELSE
        RAISE WARNING 'intercity_corridors has rows without coordinates; NOT NULL not applied';
    END IF;
END $$;

ANALYZE intercity_corridors;
