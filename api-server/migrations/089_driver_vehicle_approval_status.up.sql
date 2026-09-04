-- Per-vehicle approval, mirroring driver_profiles.approval_status.
--
-- A driver may own several vehicles but drives exactly one at a time
-- (driver_vehicles.is_active). Before this migration there was no review gate
-- on a VEHICLE at all, only on the driver as a whole: a newly added second
-- vehicle could be activated and driven the moment it was created, on
-- completely unreviewed papers. Worse, uploading THAT vehicle's documents
-- reopened the WHOLE driver for review (internal/driver.UploadDocument ->
-- reopenForReview), force-evicting them from a DIFFERENT vehicle they were
-- already approved and actively earning on. This column, plus the
-- application-layer changes that gate on it, close both gaps.
--
-- approval_status is the per-vehicle counterpart of
-- driver_profiles.approval_status, deliberately a NARROWER vocabulary
-- (PENDING_REVIEW / APPROVED / REJECTED only -- no NEEDS_MORE_INFO or
-- SUSPENDED at the vehicle level; those remain whole-driver concepts that a
-- vehicle's own paperwork status has no reason to duplicate).

BEGIN;

ALTER TABLE driver_vehicles
  ADD COLUMN IF NOT EXISTS approval_status VARCHAR(20) NOT NULL DEFAULT 'PENDING_REVIEW'
    CHECK (approval_status IN ('PENDING_REVIEW', 'APPROVED', 'REJECTED')),
  ADD COLUMN IF NOT EXISTS rejection_reason TEXT;

COMMENT ON COLUMN driver_vehicles.approval_status IS
  'Per-vehicle review gate, independent of driver_profiles.approval_status. A vehicle must be APPROVED before it can be activated (internal/driver.Service.ActivateVehicle) or driven online as the active vehicle (internal/driver.Service.SetAvailability).';
COMMENT ON COLUMN driver_vehicles.rejection_reason IS
  'Set by an admin rejecting this specific vehicle (POST /admin/drivers/{id}/vehicles/{vehicleId}/reject). Cleared on approval.';

-- Backfill: a vehicle belonging to a driver who is ALREADY approved today
-- keeps working -- set it APPROVED so nobody currently earning is disrupted
-- by this migration landing. Every other vehicle (driver not yet approved,
-- rejected, suspended, or needing more info) starts PENDING_REVIEW, which is
-- already the column default and needs no explicit UPDATE.
--
-- Verified against the three shapes that exist in production data:
--   - driver APPROVED with 1 vehicle  -> that vehicle becomes APPROVED here.
--   - driver PENDING_REVIEW/other with 1 vehicle -> stays PENDING_REVIEW (default, untouched).
--   - driver with 0 driver_vehicles rows (pre-migration-029 driver who never
--     triggered the lazy backfill in internal/driver.Service.ListVehicles) ->
--     nothing to update here; the backfill that eventually creates that row
--     (ListVehicles, or admin's resolveVehicleForDocument) inherits the SAME
--     rule at creation time -- see internal/driver/vehicles.go and
--     internal/admin/drivers.go.
UPDATE driver_vehicles dv
SET approval_status = 'APPROVED'
FROM driver_profiles dp
WHERE dp.id = dv.driver_id
  AND dp.approval_status = 'APPROVED';

CREATE INDEX IF NOT EXISTS idx_driver_vehicles_approval_status
  ON driver_vehicles (approval_status);

COMMIT;
