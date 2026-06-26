-- Seed the default launch ride-packages for every vehicle type so a fresh DB has
-- a working catalog. The "Launch Starter Package" is price 0 + promotional, which
-- also serves as the package the dev free-trial auto-grant looks for
-- (is_promotional = TRUE AND price_rwf = 0). Idempotent: only inserts a package
-- that isn't already present for that vehicle type, so it's a no-op on DBs that
-- already have them.
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional, is_active, cost_per_ride_rwf)
SELECT 'Launch Starter Package', vt.id, 35, 30, 0, TRUE, TRUE, 0
FROM vehicle_types vt
WHERE NOT EXISTS (
  SELECT 1 FROM ride_packages rp WHERE rp.vehicle_type_id = vt.id AND rp.name = 'Launch Starter Package'
);

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional, is_active, cost_per_ride_rwf)
SELECT 'Growth Package', vt.id, 75, 30, 2000, FALSE, TRUE, 27
FROM vehicle_types vt
WHERE NOT EXISTS (
  SELECT 1 FROM ride_packages rp WHERE rp.vehicle_type_id = vt.id AND rp.name = 'Growth Package'
);

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional, is_active, cost_per_ride_rwf)
SELECT 'Pro Package', vt.id, 150, 30, 3500, FALSE, TRUE, 23
FROM vehicle_types vt
WHERE NOT EXISTS (
  SELECT 1 FROM ride_packages rp WHERE rp.vehicle_type_id = vt.id AND rp.name = 'Pro Package'
);
