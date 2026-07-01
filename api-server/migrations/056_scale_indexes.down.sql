-- 056_scale_indexes.down.sql
-- Reverts index optimizations introduced in 056_scale_indexes.up.sql.

DROP INDEX IF EXISTS idx_rides_active;
DROP INDEX IF EXISTS idx_rides_driver_status;
DROP INDEX IF EXISTS idx_rides_customer_status;
DROP INDEX IF EXISTS idx_negotiation_rounds_ride_round;
DROP INDEX IF EXISTS idx_ride_events_ride_occurred;
DROP INDEX IF EXISTS idx_driver_profiles_online_matching;
