-- Reverse of 096. Drop in dependency order: bookings reference trips, trips
-- reference corridors.
DROP TABLE IF EXISTS intercity_bookings;
DROP TABLE IF EXISTS intercity_trips;
DROP TABLE IF EXISTS intercity_corridors;
