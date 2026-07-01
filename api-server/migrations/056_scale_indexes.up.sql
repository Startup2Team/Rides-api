-- 056_scale_indexes.up.sql
-- Optimizes active queries and hot-path lookups for high scaling (150M+ users).

-- 1. Partial index on active rides (for matching, reconnect, and active ride tracking)
CREATE INDEX IF NOT EXISTS idx_rides_active 
ON rides (status, driver_id, customer_id) 
WHERE status NOT IN ('COMPLETED', 'CANCELLED');

-- 2. Composite indexes for historical ride lists and queries filtering on driver/customer status
CREATE INDEX IF NOT EXISTS idx_rides_driver_status 
ON rides (driver_id, status, created_at DESC) 
WHERE driver_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_rides_customer_status 
ON rides (customer_id, status, created_at DESC);

-- 3. Composite index on negotiation rounds to speed up round queries during ride bidding
CREATE INDEX IF NOT EXISTS idx_negotiation_rounds_ride_round 
ON negotiation_rounds (ride_id, round_number);

-- 4. Composite index on ride events for fast sequential event logs loading
CREATE INDEX IF NOT EXISTS idx_ride_events_ride_occurred 
ON ride_events (ride_id, occurred_at ASC);

-- 5. Partial index on online + approved drivers (used heavily by matching engine and dashboard stats)
CREATE INDEX IF NOT EXISTS idx_driver_profiles_online_matching 
ON driver_profiles (id, transport_type) 
WHERE is_online = TRUE AND approval_status = 'APPROVED';
