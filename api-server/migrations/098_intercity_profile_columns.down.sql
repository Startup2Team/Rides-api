-- Reverse of 098.
-- DEPLOY ORDER: roll back the BINARY FIRST, then this migration. Dropping
-- intercity_committed_until while a binary that selects it is still serving
-- makes every FindNearby call error, which kills the fallback dispatch path for
-- ordinary city rides.
ALTER TABLE driver_profiles   DROP COLUMN IF EXISTS intercity_committed_until;
ALTER TABLE customer_profiles DROP COLUMN IF EXISTS no_show_count;
