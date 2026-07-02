package driver

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Vehicle is a row in driver_vehicles.
type Vehicle struct {
	ID              string    `json:"id"`
	DriverID        string    `json:"driver_id"`
	VehicleTypeID   string    `json:"vehicle_type_id"`
	VehicleTypeCode string    `json:"vehicle_type_code"`
	PlateNumber     string    `json:"plate_number"`
	Make            *string   `json:"make,omitempty"`
	Model           *string   `json:"model,omitempty"`
	Year            *int      `json:"year,omitempty"`
	Color           *string   `json:"color,omitempty"`
	PassengerSeats  *int      `json:"passenger_seats,omitempty"`
	LoadCapacityKg  *float64  `json:"load_capacity_kg,omitempty"`
	IsActive        bool      `json:"is_active"`
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
		&v.PassengerSeats, &v.LoadCapacityKg, &v.IsActive, &v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return v, nil
}

const vehicleSelectCols = `
	dv.id, dv.driver_id, dv.vehicle_type_id, vt.code,
	dv.plate_number, dv.make, dv.model, dv.year, dv.color,
	dv.passenger_seats, dv.load_capacity_kg, dv.is_active, dv.created_at, dv.updated_at
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
	return err
}

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
		var nextID string
		if err := r.db.QueryRow(ctx, `
			SELECT id FROM driver_vehicles WHERE driver_id = $1 ORDER BY created_at ASC LIMIT 1
		`, driverProfileID).Scan(&nextID); err == nil {
			_, _ = r.ActivateVehicle(ctx, driverProfileID, nextID)
		}
	}
	return nil
}

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

func (s *Service) ListVehicles(ctx context.Context, userID string) ([]*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVehicles(ctx, profile.ID)
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
	return s.repo.UpdateVehicle(ctx, profile.ID, vehicleID, in)
}

func (s *Service) DeleteVehicle(ctx context.Context, userID, vehicleID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.DeleteVehicle(ctx, profile.ID, vehicleID)
}

func (s *Service) ActivateVehicle(ctx context.Context, userID, vehicleID string) (*Vehicle, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ActivateVehicle(ctx, profile.ID, vehicleID)
}
