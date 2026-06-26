-- Backfill ACTIVE ride_package_versions for packages that were inserted without one
-- (for example migration 048 seed rows on a fresh database).
INSERT INTO ride_package_versions (
  package_id,
  version_number,
  rides,
  bonus_rides,
  price_rwf,
  cost_per_ride_rwf,
  validity_days,
  is_promotional,
  status,
  active_from
)
SELECT
  rp.id,
  1,
  rp.ride_count,
  COALESCE(rp.bonus_rides, 0),
  rp.price_rwf,
  COALESCE(rp.cost_per_ride_rwf, 0),
  rp.validity_days,
  rp.is_promotional,
  'ACTIVE',
  COALESCE(rp.created_at, NOW())
FROM ride_packages rp
WHERE rp.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM ride_package_versions rpv
    WHERE rpv.package_id = rp.id
      AND rpv.status = 'ACTIVE'
  );
