-- Separate a driver's CURRENT VIEW (customer/driver) from their CAPABILITY
-- (role_state). Switching view now flips preferred_mode; role_state stays the
-- capability and is reconciled from driver_profiles, so a switch can never
-- erase driver-hood ("all drivers are customers, not all customers are drivers").
ALTER TABLE users ADD COLUMN preferred_mode TEXT;
