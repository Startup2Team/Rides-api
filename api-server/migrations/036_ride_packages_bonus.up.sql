-- Bonus credits granted on top of ride_count, mirrors the mobile package model
-- (includedRides + bonusRides = totalCredits).
ALTER TABLE ride_packages
    ADD COLUMN IF NOT EXISTS bonus_rides INTEGER NOT NULL DEFAULT 0;
