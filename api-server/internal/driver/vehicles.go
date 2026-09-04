package driver

// Multi-vehicle support: a driver registers one or more vehicles in
// driver_vehicles and exactly one is active at a time. The active vehicle is
// what matching uses (ActivateVehicle syncs driver_profiles.transport_type)
// and what per-vehicle package credits resolve against. Ported from the dev
// branch (PR #56) with the production business rules added:
//   - activation requires an APPROVED driver profile,
//   - activation is blocked while the driver has a ride in progress,
//   - ListVehicles lazily backfills a vehicle row from the profile for
//     drivers who applied before this table was written to.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Per-vehicle approval-status values (migration 089) — the per-vehicle
// counterpart of the driver-level constants in service.go
// (StatusPendingReview/StatusApproved/StatusRejected). Deliberately a
// NARROWER vocabulary than the driver machine: no NEEDS_MORE_INFO or
// SUSPENDED at the vehicle level, enforced by a CHECK constraint in the
// migration, not just convention here.
const (
	VehicleStatusPendingReview = "PENDING_REVIEW"
	VehicleStatusApproved      = "APPROVED"
	VehicleStatusRejected      = "REJECTED"
)

// Vehicle is a row in driver_vehicles.
type Vehicle struct {
	ID              string   `json:"id"`
	DriverID        string   `json:"driver_id"`
	VehicleTypeID   string   `json:"vehicle_type_id"`
	VehicleTypeCode string   `json:"vehicle_type_code"`
	PlateNumber     string   `json:"plate_number"`
	Make            *string  `json:"make,omitempty"`
	Model           *string  `json:"model,omitempty"`
	Year            *int     `json:"year,omitempty"`
	Color           *string  `json:"color,omitempty"`
	PassengerSeats  *int     `json:"passenger_seats,omitempty"`
	LoadCapacityKg  *float64 `json:"load_capacity_kg,omitempty"`
	IsActive        bool     `json:"is_active"`
	// ApprovalStatus gates activation (Service.ActivateVehicle) and going
	// online while this is the active vehicle (Service.SetAvailability) — see
	// the VehicleStatus* constants above.
	ApprovalStatus  string    `json:"approval_status"`
	RejectionReason *string   `json:"rejection_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CreateVehicleInput struct {
	VehicleTypeCode string   `json:"vehicle_type_code" validate:"required,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
	PlateNumber     string   `json:"plate_number" validate:"required"`
	Make            *string  `json:"make"`
	Model           *string  `json:"model"`
	Year            *int     `json:"year"`
	Color           *string  `json:"color"`
	PassengerSeats  *int     `json:"passenger_seats"`
	LoadCapacityKg  *float64 `json:"load_capacity_kg"`
	LicenseNumber   *string  `json:"license_number"`
}

type UpdateVehicleInput struct {
	PlateNumber    *string  `json:"plate_number"`
	Make           *string  `json:"make"`
	Model          *string  `json:"model"`
	Year           *int     `json:"year"`
	Color          *string  `json:"color"`
	PassengerSeats *int     `json:"passenger_seats"`
	LoadCapacityKg *float64 `json:"load_capacity_kg"`
}

func (r *Repository) lookupVehicleTypeID(ctx context.Context, code string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1 AND is_active = TRUE`, code).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperrors.ErrBadRequest
		}
		return "", err
	}
	return id, nil
}

func scanVehicle(row pgx.Row) (*Vehicle, error) {
	v := &Vehicle{}
	err := row.Scan(
		&v.ID, &v.DriverID, &v.VehicleTypeID, &v.VehicleTypeCode,
		&v.PlateNumber, &v.Make, &v.Model, &v.Year, &v.Color,
		&v.PassengerSeats, &v.LoadCapacityKg, &v.IsActive,
		&v.ApprovalStatus, &v.RejectionReason, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return v, nil
}

const vehicleSelectCols = `
	dv.id, dv.driver_id, dv.vehicle_type_id, vt.code,
	dv.plate_number, dv.make, dv.model, dv.year, dv.color,
	dv.passenger_seats, dv.load_capacity_kg, dv.is_active,
	dv.approval_status, dv.rejection_reason, dv.created_at, dv.updated_at
`

func (r *Repository) ListVehicles(ctx context.Context, driverProfileID string) ([]*Vehicle, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+vehicleSelectCols+`
		FROM driver_vehicles dv
		JOIN vehicle_types vt ON vt.id = dv.vehicle_type_id
		WHERE dv.driver_id = $1
		ORDER BY dv.is_active DESC, dv.created_at ASC
	`, driverProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	if list == nil {
		list = []*Vehicle{}
	}
	return list, rows.Err()
}

func (r *Repository) GetVehicle(ctx context.Context, driverProfileID, vehicleID string) (*Vehicle, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+vehicleSelectCols+`
		FROM driver_vehicles dv
		JOIN vehicle_types vt ON vt.id = dv.vehicle_type_id
		WHERE dv.id = $1 AND dv.driver_id = $2
	`, vehicleID, driverProfileID)
	v, err := scanVehicle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperrors.ErrNotFound
	}
	return v, err
}

func (r *Repository) CreateVehicle(ctx context.Context, driverProfileID string, in CreateVehicleInput, setActive bool) (*Vehicle, error) {
	typeID, err := r.lookupVehicleTypeID(ctx, in.VehicleTypeCode)
	if err != nil {
		return nil, err
	}
	if setActive {
		if _, err := r.db.Exec(ctx, `UPDATE driver_vehicles SET is_active = FALSE, updated_at = NOW() WHERE driver_id = $1`, driverProfileID); err != nil {
			return nil, err
		}
	}
	var id string
	err = r.db.QueryRow(ctx, `
		INSERT INTO driver_vehicles (
			driver_id, vehicle_type_id, plate_number, make, model, year, color,
			passenger_seats, load_capacity_kg, is_active
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id
	`, driverProfileID, typeID, in.PlateNumber, in.Make, in.Model, in.Year, in.Color,
		in.PassengerSeats, in.LoadCapacityKg, setActive).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apperrors.New(409, "DUPLICATE_PLATE", "vehicle plate already registered")
		}
		return nil, err
	}
	return r.GetVehicle(ctx, driverProfileID, id)
}

// CreateVehicleFromApply mirrors a new driver application into driver_vehicles
// so the vehicle list and per-vehicle credits work from day one.
func (r *Repository) CreateVehicleFromApply(ctx context.Context, profileID string, in ApplyInput) error {
	seats := in.PassengerSeats
	var load *float64
	if in.LoadCapacityKg != nil {
		v := float64(*in.LoadCapacityKg)
		load = &v
	}
	_, err := r.CreateVehicle(ctx, profileID, CreateVehicleInput{
		VehicleTypeCode: in.TransportType,
		PlateNumber:     in.VehiclePlate,
		PassengerSeats:  seats,
		LoadCapacityKg:  load,
	}, true)
	if err != nil && isUniqueViolation(err) {
		return nil
	}
	return err
}

// safetyFieldChanged is the WHERE/CASE fragment shared by UpdateVehicle's
// approval_status and rejection_reason resets below: true when the caller
// actually supplied a SAFETY-RELEVANT field (plate number or either capacity
// measure — vehicle type isn't editable via UpdateVehicleInput at all, so it
// can't drift here) AND the new value differs from what's stored. A no-op
// edit (same plate resubmitted, or only cosmetic fields like make/model/
// year/color touched) must not bounce an already-approved vehicle back into
// review. IS DISTINCT FROM is the null-safe equality Postgres needs here
// (in.PassengerSeats/in.LoadCapacityKg can themselves be NULL).
const safetyFieldChanged = `(
	($3 IS NOT NULL AND $3 IS DISTINCT FROM plate_number) OR
	($8 IS NOT NULL AND $8 IS DISTINCT FROM passenger_seats) OR
	($9 IS NOT NULL AND $9 IS DISTINCT FROM load_capacity_kg)
)`

// UpdateVehicle applies a driver's edit and, in the SAME statement, resets
// approval_status back to PENDING_REVIEW (clearing any prior
// rejection_reason) whenever a safety-relevant field actually changed — see
// safetyFieldChanged. Doing this as a CASE in the UPDATE itself (rather than
// a read-diff-write in Go) keeps it atomic against a concurrent PATCH on the
// same vehicle and costs no extra round trip. Cosmetic-only edits (make,
// model, year, color) leave approval_status untouched, so a driver fixing a
// typo in the vehicle color doesn't lose an already-APPROVED status.
//
// Service.UpdateVehicle is responsible for evicting an online driver if this
// reset lands on their currently ACTIVE vehicle (evictIfActiveVehicleNotApproved).
func (r *Repository) UpdateVehicle(ctx context.Context, driverProfileID, vehicleID string, in UpdateVehicleInput) (*Vehicle, error) {
	if _, err := r.GetVehicle(ctx, driverProfileID, vehicleID); err != nil {
		return nil, err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE driver_vehicles SET
			plate_number = COALESCE($3, plate_number),
			make = COALESCE($4, make),
			model = COALESCE($5, model),
			year = COALESCE($6, year),
			color = COALESCE($7, color),
			passenger_seats = COALESCE($8, passenger_seats),
			load_capacity_kg = COALESCE($9, load_capacity_kg),
			approval_status = CASE WHEN `+safetyFieldChanged+` THEN 'PENDING_REVIEW' ELSE approval_status END,
			rejection_reason = CASE WHEN `+safetyFieldChanged+` THEN NULL ELSE rejection_reason END,
			updated_at = NOW()
		WHERE id = $1 AND driver_id = $2
	`, vehicleID, driverProfileID, in.PlateNumber, in.Make, in.Model, in.Year, in.Color, in.PassengerSeats, in.LoadCapacityKg)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apperrors.New(409, "DUPLICATE_PLATE", "vehicle plate already registered")
		}
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, apperrors.ErrNotFound
	}
	return r.GetVehicle(ctx, driverProfileID, vehicleID)
}

func (r *Repository) DeleteVehicle(ctx context.Context, driverProfileID, vehicleID string) error {
	v, err := r.GetVehicle(ctx, driverProfileID, vehicleID)
	if err != nil {
		return err
	}
	count := 0
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_vehicles WHERE driver_id = $1`, driverProfileID).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return apperrors.New(409, "LAST_VEHICLE", "cannot delete the only vehicle on file")
	}
	tag, err := r.db.Exec(ctx, `DELETE FROM driver_vehicles WHERE id = $1 AND driver_id = $2`, vehicleID, driverProfileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	if v.IsActive {
		// Prefer the oldest remaining APPROVED vehicle so an online driver lands
		// on one they can legally keep working on; only fall back to the oldest
		// vehicle overall (approved or not) if none is approved, preserving the
		// "always place the pointer somewhere" invariant ActivateVehicle's doc
		// comment describes. Service.DeleteVehicle checks the outcome and
		// force-offlines an online driver if the reassignment still landed on a
		// non-APPROVED vehicle — this query alone cannot see is_online.
		var nextID string
		if err := r.db.QueryRow(ctx, `
			SELECT id FROM driver_vehicles WHERE driver_id = $1
			ORDER BY (approval_status = 'APPROVED') DESC, created_at ASC LIMIT 1
		`, driverProfileID).Scan(&nextID); err == nil {
			_, _ = r.ActivateVehicle(ctx, driverProfileID, nextID)
		}
	}
	return nil
}

// ActivateVehicle makes one vehicle the active one and syncs the denormalised
// vehicle fields on driver_profiles (transport_type drives matching) in the
// same transaction, so matching can never see a half-switched driver.
func (r *Repository) ActivateVehicle(ctx context.Context, driverProfileID, vehicleID string) (*Vehicle, error) {
	v, err := r.GetVehicle(ctx, driverProfileID, vehicleID)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE driver_vehicles SET is_active = FALSE, updated_at = NOW() WHERE driver_id = $1`, driverProfileID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_vehicles SET is_active = TRUE, updated_at = NOW() WHERE id = $1 AND driver_id = $2`, vehicleID, driverProfileID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE driver_profiles SET
			transport_type = $2,
			vehicle_plate = $3,
			passenger_seats = $4,
			load_capacity_kg = $5,
			updated_at = NOW()
		WHERE id = $1
	`, driverProfileID, v.VehicleTypeCode, v.PlateNumber, v.PassengerSeats, v.LoadCapacityKg); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetVehicle(ctx, driverProfileID, vehicleID)
}

// SetVehicleApprovalStatus is the admin-facing per-vehicle review decision
// (POST /admin/drivers/{id}/vehicles/{vehicleId}/approve|reject) — sets ONE
// vehicle's approval_status, leaving the driver's own driver_profiles.
// approval_status and every other vehicle of theirs untouched. This is what
// lets a driver keep earning on an already-approved vehicle while a second
// one is reviewed independently.
func (r *Repository) SetVehicleApprovalStatus(ctx context.Context, vehicleID, status string, rejectionReason *string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE driver_vehicles
		SET approval_status = $1, rejection_reason = $2, updated_at = NOW()
		WHERE id = $3
	`, status, rejectionReason, vehicleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// SetActiveVehicleApprovalStatus syncs the driver's currently ACTIVE
// vehicle's approval_status to match a DRIVER-level approval transition. Used
// only by callers that just approved the driver as a whole (admin.
// ApproveDriver's production path, and Apply's DEV_AUTO_APPROVE_DRIVERS
// shortcut) — without this, every driver's very first vehicle would be born
// PENDING_REVIEW (the column default) and stay that way forever, since
// nothing else ever reviews it once the one-time driver-level approval has
// already happened. It is deliberately one-directional: rejecting or
// suspending a DRIVER does not cascade to REJECT their vehicle — a vehicle's
// own paperwork does not become invalid just because the driver's account
// was suspended, and un-suspending must not silently re-approve a vehicle an
// admin separately rejected.
//
// 0 rows affected (a driver with no driver_vehicles row yet, e.g. a legacy
// profile that predates driver_vehicles and hasn't hit the lazy backfill in
// Service.ListVehicles) is not an error — there is nothing to sync yet, and
// the eventual backfill inherits the driver's approval status directly (see
// Service.ListVehicles and internal/admin's resolveVehicleForDocument).
func (r *Repository) SetActiveVehicleApprovalStatus(ctx context.Context, driverProfileID, status string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_vehicles SET approval_status = $1, updated_at = NOW()
		WHERE driver_id = $2 AND is_active = TRUE
	`, status, driverProfileID)
	return err
}

// ── Service layer ─────────────────────────────────────────────────────────────

// ListVehicles returns the driver's vehicles. Drivers who applied before
// driver_vehicles was written to have a profile but no vehicle rows — for them
// we lazily backfill one row from the profile so the list, switching and
// per-vehicle credits all work without a data migration.
func (s *Service) ListVehicles(ctx context.Context, userID string) ([]*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	list, err := s.repo.ListVehicles(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 && profile.TransportType != "" && profile.VehiclePlate != "" {
		var load *float64
		if profile.LoadCapacityKg != nil {
			v := float64(*profile.LoadCapacityKg)
			load = &v
		}
		if _, err := s.repo.CreateVehicle(ctx, profile.ID, CreateVehicleInput{
			VehicleTypeCode: profile.TransportType,
			PlateNumber:     profile.VehiclePlate,
			PassengerSeats:  profile.PassengerSeats,
			LoadCapacityKg:  load,
		}, true); err != nil {
			// Backfill is best-effort (e.g. plate scrubbed after account deletion) —
			// return the empty list rather than failing the read.
			s.log.Warn().Err(err).Str("driver_profile_id", profile.ID).Msg("vehicles: lazy backfill from profile failed")
			return list, nil
		}
		// The freshly-created row is born PENDING_REVIEW (the column default).
		// That is correct for a driver who is not yet approved, but WRONG for a
		// driver who is already APPROVED and has been working on this exact
		// vehicle all along — migration 089's backfill covers every
		// driver_vehicles row that existed AT DEPLOY TIME, but this lazy path
		// creates a row LATER, for a driver profile that predates
		// driver_vehicles entirely and never triggered it before. Without this
		// sync, an already-working driver would be newly blocked from going
		// online (VEHICLE_NOT_APPROVED) the moment their vehicle row happens to
		// get backfilled — exactly the disruption this feature must not cause.
		if profile.ApprovalStatus == StatusApproved {
			if verr := s.repo.SetActiveVehicleApprovalStatus(ctx, profile.ID, VehicleStatusApproved); verr != nil {
				s.log.Warn().Err(verr).Str("driver_profile_id", profile.ID).
					Msg("vehicles: backfilled vehicle row but could not sync its approval status to the already-approved driver")
			}
		}
		s.log.Info().Str("driver_profile_id", profile.ID).Msg("vehicles: backfilled vehicle row from legacy profile")
		return s.repo.ListVehicles(ctx, profile.ID)
	}
	return list, nil
}

func (s *Service) CreateVehicle(ctx context.Context, userID string, in CreateVehicleInput) (*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	vehicles, _ := s.repo.ListVehicles(ctx, profile.ID)
	setActive := len(vehicles) == 0
	return s.repo.CreateVehicle(ctx, profile.ID, in, setActive)
}

func (s *Service) UpdateVehicle(ctx context.Context, userID, vehicleID string, in UpdateVehicleInput) (*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Fetched up front (not just for the active-ride check): also needed after
	// the update to know whether the JUST-EDITED vehicle is the driver's active
	// one, for the evict-if-now-unapproved check below.
	before, gErr := s.repo.GetVehicle(ctx, profile.ID, vehicleID)
	if gErr != nil {
		return nil, gErr
	}
	// Editing the active vehicle's identity (plate/type/capacity) mid-ride would
	// change the vehicle the customer agreed to — block it, same rule as switching.
	if before.IsActive && s.repo.HasActiveRide(ctx, userID) {
		return nil, apperrors.New(409, "VEHICLE_LOCKED_ON_RIDE", "You cannot edit the active vehicle during an active ride.")
	}
	updated, err := s.repo.UpdateVehicle(ctx, profile.ID, vehicleID, in)
	if err != nil {
		return nil, err
	}
	// Repository.UpdateVehicle resets approval_status to PENDING_REVIEW when a
	// safety-relevant field (plate/capacity) actually changed. If that just
	// happened to the driver's ACTIVE vehicle and they're currently online,
	// they must not keep matching on a now-unreviewed vehicle — evict exactly
	// like DeleteVehicle and admin.RejectVehicle already do for the same
	// situation (P1 from the per-vehicle-approval review).
	if before.IsActive && updated.ApprovalStatus != VehicleStatusApproved && profile.IsOnline {
		s.evictIfActiveVehicleNotApproved(ctx, profile)
	}
	return updated, nil
}

func (s *Service) DeleteVehicle(ctx context.Context, userID, vehicleID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	// Fetched up front (not just inside the active-ride branch below) because
	// the post-delete eviction check further down also needs to know whether
	// the deleted vehicle was the active one.
	target, gErr := s.repo.GetVehicle(ctx, profile.ID, vehicleID)
	if gErr != nil {
		return gErr
	}
	// Deleting the active vehicle auto-activates another one — that is a vehicle
	// switch, which is forbidden during an active ride (bypasses ActivateVehicle's
	// guard otherwise).
	if target.IsActive && s.repo.HasActiveRide(ctx, userID) {
		return apperrors.New(409, "VEHICLE_LOCKED_ON_RIDE", "You cannot remove the active vehicle during an active ride.")
	}
	if err := s.repo.DeleteVehicle(ctx, profile.ID, vehicleID); err != nil {
		return err
	}
	// Repository.DeleteVehicle auto-reassigns is_active to another vehicle when
	// the deleted one was active, preferring an APPROVED vehicle but falling
	// back to any vehicle if none is approved (see its doc comment). If the
	// driver is currently online, that reassignment must not silently land
	// them on a non-APPROVED vehicle while they stay pinned in the matching
	// geo index (P1 from the per-vehicle-approval review, same closure as
	// admin.Service.RejectVehicle's eviction for an admin-rejected active
	// vehicle) — re-check and evict if needed.
	if target.IsActive && profile.IsOnline {
		s.evictIfActiveVehicleNotApproved(ctx, profile)
	}
	return nil
}

// evictIfActiveVehicleNotApproved re-reads the driver's (possibly just
// reassigned by DeleteVehicle) active vehicle's approval status and, if it is
// not APPROVED, force-offlines the driver the same way reopenForReview and
// SuspendDriver (internal/admin/drivers.go) already do: Postgres
// is_online=false, Redis DriverState+geo ZRem (evictOnlineDriverFromRedis),
// and an FCM notification — WS alone never reaches a closed app. profile is
// the PRE-delete profile (already confirmed online by the caller); its
// TransportType is what the driver was actually pinned under in the Redis geo
// index when they went online, which is what must be cleared regardless of
// what driver_profiles.transport_type reads after the reassignment.
// Best-effort/logged: the vehicle deletion itself already succeeded and must
// not be reported as failed because of a downstream Redis/notify hiccup.
func (s *Service) evictIfActiveVehicleNotApproved(ctx context.Context, profile *Profile) {
	status, err := s.repo.GetActiveVehicleApprovalStatus(ctx, profile.ID)
	if err != nil {
		s.log.Error().Err(err).Str("driver_profile_id", profile.ID).
			Msg("driver: deleted active vehicle but could not re-check the reassigned vehicle's approval status")
		return
	}
	if status == "" || status == VehicleStatusApproved {
		return
	}
	if err := s.repo.UpdateOnlineStatus(ctx, profile.UserID, false); err != nil {
		s.log.Error().Err(err).Str("driver_profile_id", profile.ID).
			Msg("driver: deleted active vehicle but could not force is_online=false after reassigning to a non-approved vehicle")
	}
	s.evictOnlineDriverFromRedis(ctx, profile.ID, profile.TransportType)
	if s.expiryNotifier != nil {
		s.expiryNotifier.SendToAllDevices(ctx, profile.UserID, "Vehicle switch needs approval",
			"Your active vehicle was removed and the next one on file is awaiting approval, so you've been taken offline.",
			"driver", map[string]string{"type": "vehicle_reassigned_forced_offline"})
	}
}

// ActivateVehicle switches the driver's active vehicle, enforcing the
// production rules: the driver must be APPROVED, the TARGET vehicle must
// itself be APPROVED (a newly added, not-yet-reviewed vehicle cannot be put
// to work just because the driver account is fine), and switching is
// forbidden while a ride is in progress (the customer agreed to a specific
// vehicle).
//
// The approval-status gate deliberately lives here (Service), not in
// Repository.ActivateVehicle — DeleteVehicle's internal auto-reassignment of
// is_active to the next vehicle (vehicles.go, repo-to-repo call) prefers an
// APPROVED vehicle but must always be able to place the pointer somewhere
// even if none is approved; only this driver-initiated switch is gated.
// (Service.DeleteVehicle separately evicts an online driver if that
// reassignment still lands on a non-APPROVED vehicle.)
func (s *Service) ActivateVehicle(ctx context.Context, userID, vehicleID string) (*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if profile.ApprovalStatus != "APPROVED" {
		return nil, apperrors.New(403, "DRIVER_NOT_APPROVED", "Your driver account must be approved before switching vehicles.")
	}
	if s.repo.HasActiveRide(ctx, userID) {
		return nil, apperrors.New(409, "VEHICLE_SWITCH_ON_RIDE", "You cannot switch vehicles during an active ride.")
	}
	target, err := s.repo.GetVehicle(ctx, profile.ID, vehicleID)
	if err != nil {
		return nil, err
	}
	if target.ApprovalStatus != VehicleStatusApproved {
		return nil, apperrors.New(http.StatusConflict, "VEHICLE_NOT_APPROVED",
			"This vehicle is awaiting approval and cannot be activated yet.")
	}
	v, err := s.repo.ActivateVehicle(ctx, profile.ID, vehicleID)
	if err != nil {
		return nil, err
	}
	s.log.Info().
		Str("driver_profile_id", profile.ID).
		Str("vehicle_id", v.ID).
		Str("transport_type", v.VehicleTypeCode).
		Msg("driver switched active vehicle")
	return v, nil
}
