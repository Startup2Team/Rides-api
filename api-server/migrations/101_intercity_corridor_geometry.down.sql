-- Reverse of 101. Publishing will require client-supplied coordinates again.
ALTER TABLE intercity_corridors
    DROP COLUMN IF EXISTS destination_point,
    DROP COLUMN IF EXISTS origin_point;
