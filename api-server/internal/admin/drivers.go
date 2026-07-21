package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// Admin driver management: approval lifecycle, listing, documents,
// admin-created drivers, referrals and force-offline.

func (s *Service) ApproveDriver(ctx context.Context, profileID, adminUserID string) error {
	var driverUserID, transportType string
	err := s.db.QueryRow(ctx,
		`SELECT user_id, transport_type FROM driver_profiles WHERE id = $1`, profileID,
	).Scan(&driverUserID, &transportType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	if driverUserID == adminUserID {
		return apperrors.ErrSelfApproval
	}

	_, err = s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'APPROVED',
		    approved_by = $1,
		    approved_at = NOW(),
		    rejection_reason = NULL,
		    updated_at = NOW()
		WHERE id = $2 AND approval_status = 'PENDING_REVIEW'
	`, adminUserID, profileID)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
		UPDATE users u
		SET role_state = 'DRIVER_ACTIVE', updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $1 AND u.id = dp.user_id
	`, profileID)
	if err != nil {
		return err
	}

	if s.packages != nil {
		if err := s.packages.GrantFreeTrialIfEligible(ctx, driverUserID, transportType); err != nil {
			s.log.Error().Err(err).
				Str("driver_user_id", driverUserID).
				Str("transport_type", transportType).
				Msg("admin: free trial grant failed after approval")
		}
	}

	// Grant the 30-ride registration bonus (separate from the free-trial package credit).
	if s.bonus != nil {
		// Look up the vehicle_type_id for the transport_type code.
		var vehicleTypeID string
		_ = s.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, transportType).Scan(&vehicleTypeID)
		if vehicleTypeID != "" {
			if _, err := s.bonus.GrantRegistrationBonus(ctx, driverUserID, vehicleTypeID); err != nil {
				s.log.Warn().Err(err).Str("driver_user_id", driverUserID).Msg("admin: registration bonus grant failed")
			}
		}
	}
	s.revokeUserSessions(ctx, driverUserID)
	// Tell the driver they're approved (in-app + push to every device).
	if s.notifier != nil {
		s.notifier.SendToAllDevices(ctx, driverUserID, "You're approved!",
			"Your driver application has been approved. You can now go online and start accepting rides.",
			"driver", map[string]string{"type": "driver_application_approved"})
	}
	return nil
}

func (s *Service) RejectDriver(ctx context.Context, profileID, adminUserID, reason string) error {
	var driverUserID string
	if err := s.db.QueryRow(ctx,
		`SELECT user_id FROM driver_profiles WHERE id = $1`, profileID,
	).Scan(&driverUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'REJECTED',
		    approved_by = $1,
		    rejection_reason = $2,
		    updated_at = NOW()
		WHERE id = $3 AND approval_status = 'PENDING_REVIEW'
	`, adminUserID, reason, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.Newf(http.StatusConflict, "INVALID_STATE",
			"driver is not pending review or does not exist")
	}
	// Tell the driver the outcome + reason (in-app + push to every device).
	if s.notifier != nil {
		body := "Your driver application was not approved."
		if reason != "" {
			body = fmt.Sprintf("Your driver application was not approved. Reason: %s", reason)
		}
		s.notifier.SendToAllDevices(ctx, driverUserID, "Application update", body,
			"driver", map[string]string{"type": "driver_application_rejected", "reason": reason})
	}
	return nil
}

// RequestDriverMoreInfo asks the driver to resubmit documents or clarify onboarding details.
func (s *Service) RequestDriverMoreInfo(ctx context.Context, profileID, adminUserID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return apperrors.New(http.StatusBadRequest, "REASON_REQUIRED", "reason is required")
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'NEEDS_MORE_INFO',
		    approved_by = $1,
		    rejection_reason = $2,
		    updated_at = NOW()
		WHERE id = $3 AND approval_status IN ('PENDING_REVIEW', 'NEEDS_MORE_INFO')
	`, adminUserID, reason, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.Newf(http.StatusConflict, "INVALID_STATE",
			"driver is not in review or does not exist")
	}
	return nil
}

func (s *Service) SuspendDriver(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error {
	suspendedUntil := time.Now().Add(time.Duration(durationHours) * time.Hour)

	var transportType string
	err := s.db.QueryRow(ctx, `SELECT transport_type FROM driver_profiles WHERE id = $1`, profileID).Scan(&transportType)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'SUSPENDED',
		    suspension_reason = $1,
		    is_online = FALSE,
		    updated_at = NOW()
		WHERE id = $2
	`, reason, profileID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET is_suspended = TRUE,
		    suspension_until = $1,
		    role_state = 'DRIVER_SUSPENDED',
		    updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $2 AND u.id = dp.user_id
	`, suspendedUntil, profileID)
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Force offline in Redis
	if s.rdb != nil {
		s.rdb.Set(ctx, rkeys.K.DriverState(profileID), "OFFLINE", 0)
		s.rdb.ZRem(ctx, rkeys.K.DriverGeoIndex(transportType), profileID)
	}

	return nil
}

func (s *Service) ReinstateDriver(ctx context.Context, profileID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET approval_status = 'APPROVED', suspension_reason = NULL, updated_at = NOW()
		WHERE id = $1
	`, profileID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET is_suspended = FALSE, suspension_until = NULL, role_state = 'DRIVER_ACTIVE', updated_at = NOW()
		FROM driver_profiles dp
		WHERE dp.id = $1 AND u.id = dp.user_id
	`, profileID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ListDrivers returns paginated driver profiles, filterable by status, vehicle type, and search.
func (s *Service) ListDrivers(ctx context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	var wheres []string
	var args []interface{}
	n := 1

	if status != "" {
		wheres = append(wheres, fmt.Sprintf("dp.approval_status = $%d", n))
		args = append(args, status)
		n++
	}
	if vehicleType != "" {
		wheres = append(wheres, fmt.Sprintf("dp.transport_type = $%d", n))
		args = append(args, vehicleType)
		n++
	}
	if search != "" {
		wheres = append(wheres, fmt.Sprintf(
			"(u.phone_number ILIKE $%d OR u.full_name ILIKE $%d OR dp.vehicle_plate ILIKE $%d)", n, n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	base := `FROM driver_profiles dp JOIN users u ON u.id = dp.user_id`
	where := buildWhere(wheres)

	var total int
	_ = s.db.QueryRow(ctx, "SELECT COUNT(*) "+base+where, args...).Scan(&total)

	orderBy := "dp.created_at DESC"
	switch sort {
	case "acceptance_rate":
		orderBy = "dp.acceptance_rate DESC"
	case "total_rides":
		orderBy = "dp.total_rides DESC"
	case "name":
		orderBy = "u.full_name ASC"
	}

	args = append(args, limit, offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT dp.id, dp.user_id, u.phone_number, u.full_name,
		       dp.transport_type, dp.vehicle_plate, dp.approval_status,
		       dp.priority_tier, dp.total_rides, dp.acceptance_rate,
		       dp.is_online, dp.city, dp.created_at,
		       EXISTS(
		           SELECT 1 FROM rides r
		           WHERE r.driver_id = dp.id
		           AND r.status IN ('CONFIRMED','DRIVER_EN_ROUTE','DRIVER_ARRIVED','IN_PROGRESS')
		       ) AS on_trip
		%s %s ORDER BY %s LIMIT $%d OFFSET $%d
	`, base, where, orderBy, n, n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, userID, phone, transportType, plate, approvalStatus string
		var fullName *string
		var city *string
		var priorityTier, totalRides int
		var acceptanceRate float64
		var isOnline, onTrip bool
		var createdAt time.Time
		if err := rows.Scan(&id, &userID, &phone, &fullName, &transportType, &plate,
			&approvalStatus, &priorityTier, &totalRides, &acceptanceRate, &isOnline, &city, &createdAt, &onTrip); err != nil {
			return nil, 0, err
		}
		result = append(result, map[string]interface{}{
			"id": id, "user_id": userID, "phone": phone, "full_name": fullName,
			"transport_type": transportType, "vehicle_plate": plate,
			"approval_status": approvalStatus, "priority_tier": priorityTier,
			"total_rides": totalRides, "acceptance_rate": acceptanceRate,
			"is_online": isOnline, "on_trip": onTrip, "city": city, "created_at": createdAt,
		})
	}
	return result, total, nil
}

// DriverOverview returns aggregate driver status counts.
func (s *Service) DriverOverview(ctx context.Context, vehicleType string) (map[string]interface{}, error) {
	var total, online, onTrip, pending, suspended int

	// Parameterized optional filter: NULL means "all vehicle types". This keeps
	// the admin-supplied vehicleType out of the SQL string entirely.
	var vtFilter *string
	if vehicleType != "" {
		vtFilter = &vehicleType
	}

	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&total)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE is_online=TRUE AND approval_status IN ('APPROVED','ACTIVE') AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&online)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='PENDING_REVIEW' AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&pending)
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM driver_profiles WHERE approval_status='SUSPENDED' AND ($1::text IS NULL OR transport_type = $1)`, vtFilter).Scan(&suspended)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT dp.id) FROM driver_profiles dp
		JOIN rides r ON r.driver_id = dp.id
		WHERE r.status = 'IN_PROGRESS' AND ($1::text IS NULL OR dp.transport_type = $1)`, vtFilter).Scan(&onTrip)

	return map[string]interface{}{
		"total": total, "online": online,
		"on_trip": onTrip, "pending": pending, "suspended": suspended,
	}, nil
}

func (s *Service) GetDriver(ctx context.Context, profileID string) (map[string]interface{}, error) {
	var id, userID, phone, tType, plate, license, city, momoCode, approvalStatus string
	var fullName, province, district, sector, cell, village, momoProvider, merchantPayCode, suspensionReason, rejectionReason *string
	var profileImageURL *string
	var passengerSeats, loadCapacityKg *int
	var dob *time.Time
	var licenseExpiryDate, insuranceExpiryDate, authorizationExpiryDate *time.Time
	var acceptanceRate float64
	var totalRides int
	var isOnline bool
	var createdAt time.Time

	err := s.db.QueryRow(ctx, `
		SELECT dp.id, dp.user_id, u.phone_number, u.full_name, u.profile_image_url,
		       dp.transport_type, dp.vehicle_plate, dp.license_number,
		       dp.date_of_birth, dp.city,
		       dp.province, dp.district, dp.sector, dp.cell, dp.village,
		       dp.passenger_seats, dp.load_capacity_kg,
		       dp.momo_provider, dp.momo_pay_code, dp.merchant_pay_code,
		       dp.approval_status, dp.suspension_reason, dp.rejection_reason,
		       dp.acceptance_rate, dp.total_rides, dp.is_online,
		       dp.license_expiry_date, dp.insurance_expiry_date, dp.authorization_expiry_date,
		       dp.created_at
		FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		WHERE dp.id = $1
	`, profileID).Scan(
		&id, &userID, &phone, &fullName, &profileImageURL,
		&tType, &plate, &license,
		&dob, &city,
		&province, &district, &sector, &cell, &village,
		&passengerSeats, &loadCapacityKg,
		&momoProvider, &momoCode, &merchantPayCode,
		&approvalStatus, &suspensionReason, &rejectionReason,
		&acceptanceRate, &totalRides, &isOnline,
		&licenseExpiryDate, &insuranceExpiryDate, &authorizationExpiryDate,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}

	// Uploaded KYC documents (licence, national ID, insurance, authorization,
	// selfie) so the admin can review the actual photos before approving.
	docs, _ := s.listDriverDocuments(ctx, id)

	return map[string]interface{}{
		"id": id, "user_id": userID, "phone": phone, "full_name": fullName,
		"transport_type": tType, "vehicle_plate": plate, "license_number": license,
		"date_of_birth": dob, "city": city,
		"address": map[string]interface{}{
			"province": province, "district": district, "sector": sector,
			"cell": cell, "village": village,
		},
		"passenger_seats": passengerSeats, "load_capacity_kg": loadCapacityKg,
		"momo_provider": momoProvider, "momo_pay_code": momoCode,
		"merchant_pay_code": merchantPayCode, "profile_image_url": profileImageURL,
		"approval_status": approvalStatus, "suspension_reason": suspensionReason,
		"rejection_reason": rejectionReason,
		"acceptance_rate":  acceptanceRate, "total_rides": totalRides, "is_online": isOnline,
		"license_expiry_date":       licenseExpiryDate,
		"insurance_expiry_date":     insuranceExpiryDate,
		"authorization_expiry_date": authorizationExpiryDate,
		"created_at":                createdAt,
		"documents":                 docs,
	}, nil
}

func (s *Service) GetDriverReferrals(ctx context.Context, profileID string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT dp.id, COALESCE(u.full_name, ''), u.phone_number, dp.transport_type, dp.vehicle_plate, dp.approval_status, dp.created_at
		FROM driver_profiles dp
		JOIN users u ON dp.user_id = u.id
		WHERE dp.referred_by_driver_id = $1
		ORDER BY dp.created_at DESC
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var id, fullName, phone, tType, plate, approvalStatus string
		var createdAt time.Time
		if err := rows.Scan(&id, &fullName, &phone, &tType, &plate, &approvalStatus, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id":              id,
			"name":            fullName,
			"phone":           phone,
			"transport_type":  tType,
			"vehicle_plate":   plate,
			"approval_status": approvalStatus,
			"created_at":      createdAt.Format(time.RFC3339),
		})
	}
	return result, nil
}

func (s *Service) listDriverDocuments(ctx context.Context, profileID string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT document_type, file_url, uploaded_at
		FROM driver_documents WHERE driver_id = $1
		ORDER BY uploaded_at DESC
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var docType, fileURL string
		var uploadedAt time.Time
		if err := rows.Scan(&docType, &fileURL, &uploadedAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"document_type": docType,
			"file_url":      fileURL,
			"uploaded_at":   uploadedAt,
		})
	}
	return result, nil
}

func (s *Service) UpsertDriverDocument(ctx context.Context, profileID, documentType, fileURL string) error {
	if !allowedDriverDocumentTypes[documentType] {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "unsupported document_type")
	}
	if fileURL == "" {
		return apperrors.ErrBadRequest
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE id = $1)`, profileID).Scan(&exists); err != nil || !exists {
		return apperrors.ErrNotFound
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, document_type, file_url)
		VALUES ($1, $2, $3)
		ON CONFLICT (driver_id, document_type)
		DO UPDATE SET file_url = EXCLUDED.file_url, uploaded_at = NOW()
	`, profileID, documentType, fileURL)
	if err != nil {
		return err
	}
	if documentType == "PROFILE_SELFIE" {
		_, _ = s.db.Exec(ctx, `
			UPDATE users SET profile_image_url = $1, updated_at = NOW()
			WHERE id = (SELECT user_id FROM driver_profiles WHERE id = $2)
		`, fileURL, profileID)
	}
	return nil
}

func (s *Service) UpdateDriver(ctx context.Context, profileID string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	allowedFields := map[string]bool{
		"vehicle_plate":       true,
		"license_number":      true,
		"license_expiry_date": true,
		"approval_status":     true,
		"momo_pay_code":       true,
		"merchant_pay_code":   true,
		"transport_type":      true,
		"momo_provider":       true,
		"date_of_birth":       true,
		"passenger_seats":     true,
		"load_capacity_kg":    true,
	}
	for k := range fields {
		if !allowedFields[k] {
			return apperrors.New(http.StatusBadRequest, "INVALID_FIELD", "unknown or invalid field: "+k)
		}
	}

	var setClauses []string
	var args []interface{}
	n := 1
	for k, v := range fields {
		setClauses = append(setClauses, fmt.Sprintf(`"%s" = $%d`, k, n))
		args = append(args, v)
		n++
	}
	args = append(args, profileID)
	query := fmt.Sprintf("UPDATE driver_profiles SET %s, updated_at=NOW() WHERE id = $%d",
		strings.Join(setClauses, ", "), n)
	_, err := s.db.Exec(ctx, query, args...)
	return err
}

func (s *Service) DeleteDriver(ctx context.Context, profileID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM driver_profiles WHERE id = $1`, profileID)
	return err
}

// DriverDocumentInput represents a single document to attach during driver registration.
type DriverDocumentInput struct {
	DocumentType string
	FileURL      string
}

// AdminCreateDriverInput holds the payload for admin-registered drivers.
type AdminCreateDriverInput struct {
	AdminUserID     string
	FullName        string
	Phone           string
	TransportType   string
	VehiclePlate    string
	LicenseNumber   string
	DateOfBirth     string
	Province        string
	District        string
	Sector          string
	Cell            string
	Village         string
	City            string
	MomoProvider    string
	MomoPayCode     string
	MerchantPayCode string
	ProfileImageURL string
	PassengerSeats  *int
	LoadCapacityKg  *int
	Documents       []DriverDocumentInput
}

// Allowed driver document types (aligned with mobile onboarding + admin registration).
var allowedDriverDocumentTypes = map[string]bool{
	"LICENCE_FRONT":              true,
	"LICENCE_BACK":               true,
	"VEHICLE_INSURANCE":          true,
	"VEHICLE_INSURANCE_BACK":     true,
	"VEHICLE_AUTHORIZATION":      true,
	"VEHICLE_AUTHORIZATION_BACK": true,
	"PROFILE_SELFIE":             true,
}

// CreateDriverFromAdmin registers a new driver (user + profile) from the admin panel.
// If a user with the phone already exists, reuse their account.
func (s *Service) CreateDriverFromAdmin(ctx context.Context, in AdminCreateDriverInput) (map[string]interface{}, error) {
	// 1. Find or create the user record
	var userID string
	err := s.db.QueryRow(ctx,
		`SELECT id FROM users WHERE phone_number = $1`, in.Phone).Scan(&userID)
	if err != nil {
		// User not found — create one
		err = s.db.QueryRow(ctx, `
			INSERT INTO users (phone_number, full_name, role_state)
			VALUES ($1, $2, 'DRIVER_ACTIVE')
			RETURNING id`,
			in.Phone, in.FullName,
		).Scan(&userID)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	} else {
		// User exists — promote to DRIVER_ACTIVE
		_, _ = s.db.Exec(ctx,
			`UPDATE users SET role_state = 'DRIVER_ACTIVE', updated_at = NOW() WHERE id = $1`, userID)
	}

	dob := in.DateOfBirth
	if dob == "" {
		dob = "1990-01-01"
	}
	city := in.City
	if city == "" {
		city = "Kigali"
	}

	var existingProfileID string
	if err := s.db.QueryRow(ctx,
		`SELECT id FROM driver_profiles WHERE user_id = $1`, userID,
	).Scan(&existingProfileID); err == nil {
		return nil, apperrors.Newf(http.StatusConflict, "DRIVER_ALREADY_EXISTS",
			"This phone number already has a driver registration")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing driver profile: %w", err)
	}

	if in.ProfileImageURL != "" {
		_, _ = s.db.Exec(ctx,
			`UPDATE users SET profile_image_url = $1, updated_at = NOW() WHERE id = $2`,
			in.ProfileImageURL, userID)
	}

	momoCode := in.MomoPayCode
	if momoCode == "" {
		momoCode = "—"
	}
	merchantCode := in.MerchantPayCode
	momoProvider := in.MomoProvider
	if momoProvider == "" {
		momoProvider = "mtn"
	}

	// 2. Create the driver profile — admin registration is pre-approved
	var profileID string
	err = s.db.QueryRow(ctx, `
		INSERT INTO driver_profiles (
			user_id, transport_type, vehicle_plate, license_number,
			date_of_birth, city, momo_provider, momo_pay_code, merchant_pay_code,
			approval_status, approved_by, approved_at,
			province, district, sector, cell, village,
			passenger_seats, load_capacity_kg
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,'APPROVED',$10,NOW(),$11,$12,$13,$14,$15,$16,$17
		) RETURNING id`,
		userID, in.TransportType, in.VehiclePlate, in.LicenseNumber,
		dob, city, momoProvider, momoCode, merchantCode,
		in.AdminUserID,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg,
	).Scan(&profileID)
	if err != nil {
		return nil, mapAdminCreateDriverError(err, in)
	}

	// Attach documents — mirrors mobile step 2 (license, insurance, authorization)
	for _, doc := range in.Documents {
		if err := s.UpsertDriverDocument(ctx, profileID, doc.DocumentType, doc.FileURL); err != nil {
			s.log.Warn().Err(err).Str("document_type", doc.DocumentType).Msg("admin: failed to attach document during driver registration")
		}
	}

	return map[string]interface{}{
		"id":              profileID,
		"user_id":         userID,
		"transport_type":  in.TransportType,
		"vehicle_plate":   in.VehiclePlate,
		"approval_status": "APPROVED",
		"documents_saved": len(in.Documents),
		"message":         "Driver registered and approved.",
	}, nil
}

func mapAdminCreateDriverError(err error, in AdminCreateDriverInput) error {
	if err == nil {
		return apperrors.ErrInternal
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "driver_profiles_user_id_key"):
		return apperrors.Newf(http.StatusConflict, "DRIVER_ALREADY_EXISTS",
			"This phone number already has a driver registration")
	case strings.Contains(msg, "driver_profiles_vehicle_plate_key"):
		return apperrors.Newf(http.StatusConflict, "PLATE_ALREADY_EXISTS",
			"Vehicle plate %s is already registered to another driver", in.VehiclePlate)
	case strings.Contains(msg, "driver_profiles_license_number_key"):
		return apperrors.Newf(http.StatusConflict, "LICENSE_ALREADY_EXISTS",
			"Licence number %s is already registered to another driver", in.LicenseNumber)
	case strings.Contains(msg, "23505"):
		return apperrors.Newf(http.StatusConflict, "CONFLICT",
			"A driver with this phone, plate, or licence number already exists")
	default:
		return apperrors.Newf(http.StatusInternalServerError, "INTERNAL",
			"Could not create driver profile")
	}
}

// ForceDriverOffline sets is_online=false for a driver.
func (s *Service) ForceDriverOffline(ctx context.Context, profileID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE driver_profiles SET is_online = FALSE, updated_at = NOW() WHERE id = $1`, profileID)
	return err
}
