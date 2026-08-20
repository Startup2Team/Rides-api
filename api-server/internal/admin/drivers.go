package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/workspace/ride-platform/pkg/adminrole"
	"github.com/workspace/ride-platform/pkg/documents"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/nationalid"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// Admin driver management: approval lifecycle, listing, documents,
// admin-created drivers, referrals and force-offline.

// isUniqueViolation reports whether err is a Postgres unique-constraint failure.
// Matches on the SQLSTATE code rather than substring-searching the message,
// which the copies in internal/driver and internal/auth do — a message
// containing "unique" for any other reason would fool those.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isNationalIDConflict reports whether err is specifically a 23505 violation
// of uq_users_national_id, as opposed to any other unique constraint a users/
// driver_profiles write might hit (phone, plate, license). This is what lets
// "this national ID is already registered" be told apart from the generic
// driver-registration conflicts in mapAdminCreateDriverError.
func isNationalIDConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_users_national_id"
}

// errNationalIDAlreadyRegistered is the one client-facing error for a national
// ID collision — the ban-evasion / bonus-farming guard (uq_users_national_id)
// surfacing as a friendly message instead of a raw constraint violation.
func errNationalIDAlreadyRegistered() error {
	return apperrors.New(http.StatusConflict, "NATIONAL_ID_ALREADY_REGISTERED",
		"This national ID is already registered to another account.")
}

// errNationalIDMismatchOnAdminCreate is returned by CreateDriverFromAdmin
// when the existing account for the given phone number already has a
// DIFFERENT national ID on file than the one supplied. The registration
// capture is first-write-wins by design (it must not silently clobber a
// value a driver or a prior admin already set) — but for THIS admin, whose
// input would otherwise vanish with no signal, that has to be a loud,
// actionable error, not a quiet no-op. It routes them at the endpoint built
// to actually change an existing value: PATCH /admin/drivers/{id}/national-id
// (SetDriverNationalID), which is audited old-value-to-new.
func errNationalIDMismatchOnAdminCreate() error {
	return apperrors.New(http.StatusConflict, "NATIONAL_ID_MISMATCH",
		"This phone number's account already has a different national ID on file. "+
			"Use PATCH /admin/drivers/{id}/national-id to correct it.")
}

func (s *Service) ApproveDriver(ctx context.Context, profileID, adminUserID string) error {
	var driverUserID, transportType string
	var nationalIDNumber *string
	err := s.db.QueryRow(ctx,
		`SELECT dp.user_id, dp.transport_type, u.national_id_number
		   FROM driver_profiles dp
		   JOIN users u ON u.id = dp.user_id
		  WHERE dp.id = $1`, profileID,
	).Scan(&driverUserID, &transportType, &nationalIDNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	if driverUserID == adminUserID {
		return apperrors.ErrSelfApproval
	}
	// DB-1 round 2: national ID is mandatory for approval WHEN
	// NATIONAL_ID_REQUIRED is on (DB-1 staged rollout, config.go) — defensive
	// gate in case the applicant somehow reached PENDING_REVIEW without one
	// (e.g. a pre-existing profile from before this was enforced at Apply
	// time). This is what makes the uq_users_national_id uniqueness guard
	// actually prevent ban-evasion: an approved driver with no ID on file
	// can't be caught by it. Flag off (default, until mobile+web ship the
	// field) skips this so approval works exactly as before DB-1.
	if s.nationalIDRequired() && (nationalIDNumber == nil || *nationalIDNumber == "") {
		return apperrors.New(http.StatusConflict, "NATIONAL_ID_REQUIRED",
			"This driver has no national ID on file and cannot be approved.")
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
	// DB-4: the legacy bonus_grants write below is display-only (GET /bonuses) — the
	// go-online/accept credit gates and /entitlements only ever read the v4 ledger, so
	// the same amount must also land there via s.packages or the bonus is unspendable.
	if s.bonus != nil {
		// Look up the vehicle_type_id for the transport_type code.
		var vehicleTypeID string
		_ = s.db.QueryRow(ctx, `SELECT id FROM vehicle_types WHERE code = $1`, transportType).Scan(&vehicleTypeID)
		if vehicleTypeID != "" {
			bonusRides, err := s.bonus.GrantRegistrationBonus(ctx, driverUserID, vehicleTypeID)
			if err != nil {
				s.log.Warn().Err(err).Str("driver_user_id", driverUserID).Msg("admin: registration bonus grant failed")
			} else if bonusRides > 0 && s.packages != nil {
				if err := s.packages.GrantRegistrationBonus(ctx, driverUserID, vehicleTypeID, bonusRides); err != nil {
					// This is money-affecting (an unspendable promise, same bug as DB-4):
					// log loudly rather than best-effort-and-forget.
					s.log.Error().Err(err).
						Str("driver_user_id", driverUserID).
						Int("bonus_rides", bonusRides).
						Msg("admin: registration bonus ledger mirror failed — driver will see the bonus but cannot spend it")
				}
			} else if bonusRides == 0 && s.packages != nil {
				// bonusRides == 0, err == nil means either "already granted" (the
				// common case on a re-approval) or "no REGISTRATION tier configured".
				// A prior approval's ledger mirror can fail after the legacy grant
				// already landed (logged above as an Error, not fatal) — the legacy
				// side then reports "already granted" forever, so without this the
				// mirror is never retried and the driver keeps an unspendable bonus
				// until someone runs the manual backfill. Self-heal here: re-fetch
				// the tier's configured amount and retry the mirror. If the tier
				// mirror already landed, GrantRegistrationBonus is idempotent on
				// "registration:<profileID>" and this is a harmless no-op; if no
				// tier is configured, tierRides is 0 and the mirror call itself
				// no-ops (bonusRides <= 0 guard in packages.GrantRegistrationBonus).
				tierRides, err := s.bonus.RegistrationTierBonusRides(ctx)
				if err != nil {
					s.log.Warn().Err(err).Str("driver_user_id", driverUserID).
						Msg("admin: registration tier lookup failed while self-healing ledger mirror")
				} else if tierRides > 0 {
					if err := s.packages.GrantRegistrationBonus(ctx, driverUserID, vehicleTypeID, tierRides); err != nil {
						s.log.Error().Err(err).
							Str("driver_user_id", driverUserID).
							Int("bonus_rides", tierRides).
							Msg("admin: registration bonus ledger mirror self-heal failed")
					}
				}
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
	// DB-1 round 2: the same defensive gate ApproveDriver has (gated the same
	// way, on NATIONAL_ID_REQUIRED) — this also sets approval_status =
	// 'APPROVED', so while the flag is on, a legacy driver suspended before a
	// national ID was mandatory (or one an admin cleared) must not be
	// reinstated straight to APPROVED without one on file; that would let
	// uq_users_national_id's ban-evasion guard be bypassed via suspend+reinstate.
	var nationalIDNumber *string
	err := s.db.QueryRow(ctx, `
		SELECT u.national_id_number
		  FROM driver_profiles dp
		  JOIN users u ON u.id = dp.user_id
		 WHERE dp.id = $1`, profileID,
	).Scan(&nationalIDNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	}
	if s.nationalIDRequired() && (nationalIDNumber == nil || *nationalIDNumber == "") {
		return apperrors.New(http.StatusConflict, "NATIONAL_ID_REQUIRED",
			"This driver has no national ID on file and cannot be reinstated.")
	}

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

// GetDriver returns the driver detail payload for admin review. requesterRole
// gates the national ID: SuperAdmin and OpsManager see the FULL number (they
// are the roles that actually review/approve documents against it);
// SupportStaff (and any other/unknown role) get it MASKED. The caller
// (Handler.GetDriver) is responsible for auditing every VIEW where the full
// number was actually exposed.
func (s *Service) GetDriver(ctx context.Context, profileID, requesterRole string) (map[string]interface{}, error) {
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
	// National ID (DB-1): scanned unredacted here, then masked below UNLESS
	// requesterRole is SuperAdmin/OpsManager (DB-1 round 2 — the roles that
	// actually review/approve documents against it). SupportStaff and every
	// other surface (driver's own profile is full for a different reason —
	// it's their own; driver list gets nothing at all) never see the raw
	// value from this function.
	var nationalIDNumber, nationalIDCountry *string
	// Gender (admin-created-driver gender, FEAT-onboarding-fields) — appended
	// LAST in both the SELECT and Scan lists so existing positional-arg test
	// helpers (driverRowQueryRowFn) keep working unmodified: a mock row with
	// fewer values than destinations simply leaves the trailing destination
	// at its zero value (nil), which is exactly "no gender on file".
	var gender *string

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
		       dp.created_at,
		       u.national_id_number, u.national_id_country,
		       dp.gender
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
		&nationalIDNumber, &nationalIDCountry,
		&gender,
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

	// Restrict full exposure to SuperAdmin/OpsManager (DB-1 round 2) —
	// SupportStaff still sees the driver detail page, just with the number
	// masked, same shape as every other surface that isn't a document review.
	if nationalIDNumber != nil && requesterRole != adminrole.SuperAdmin && requesterRole != adminrole.OpsManager {
		masked := nationalid.Mask(*nationalIDNumber)
		nationalIDNumber = &masked
	}

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
		"national_id_number":        nationalIDNumber,
		"national_id_country":       nationalIDCountry,
		"gender":                    gender,
	}, nil
}

// SetDriverNationalID is the ONLY path (besides the driver's own pre-approval
// self-correction, internal/driver.SetOwnNationalID) that can set or change a
// driver's national ID — restricted to SuperAdmin/OpsManager at the route
// gate (SupportStaff removed, DB-1 round 2). The caller (handler layer) is
// responsible for writing the admin_audit_log entry using the MASKED old and
// new numbers this returns — the raw numbers are never logged, and recording
// both (not new-only) is what makes a correction reviewable after the fact.
//
// Unlike the driver-onboarding capture (first-write-wins, see
// setUserNationalIDTx in internal/driver), this write is UNCONDITIONAL — an
// admin is explicitly authorised to correct an already-captured value, which
// is the whole point of this endpoint existing.
func (s *Service) SetDriverNationalID(ctx context.Context, profileID, country, number string) (maskedOld, maskedNew, normCountry string, err error) {
	var userID string
	var oldNumber *string
	if err := s.db.QueryRow(ctx,
		`SELECT dp.user_id, u.national_id_number
		   FROM driver_profiles dp
		   JOIN users u ON u.id = dp.user_id
		  WHERE dp.id = $1`, profileID,
	).Scan(&userID, &oldNumber); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", apperrors.ErrNotFound
		}
		return "", "", "", err
	}
	if oldNumber != nil {
		maskedOld = nationalid.Mask(*oldNumber)
	}

	normCountry, normNumber := nationalid.Normalize(country, number)
	if verr := nationalid.Validate(normCountry, normNumber); verr != nil {
		return "", "", "", apperrors.New(http.StatusBadRequest, "INVALID_NATIONAL_ID", verr.Error())
	}

	if _, err := s.db.Exec(ctx, `
		UPDATE users SET national_id_number = $1, national_id_country = $2, updated_at = NOW()
		WHERE id = $3
	`, normNumber, normCountry, userID); err != nil {
		if isNationalIDConflict(err) {
			return "", "", "", errNationalIDAlreadyRegistered()
		}
		return "", "", "", err
	}

	return maskedOld, nationalid.Mask(normNumber), normCountry, nil
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

// listDriverDocuments returns the LIVE version of each document.
//
// The superseded_at filter matters: documents are append-only since migration
// 077, so without it the review screen would list every historical upload and
// show the same document_type several times, with no indication which one counts.
//
// review_status and sha256 come along so the reviewer can see whether this exact
// file has been looked at, and can quote a digest when it is disputed.
func (s *Service) listDriverDocuments(ctx context.Context, profileID string) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(ctx, `
		SELECT document_type, file_url, uploaded_at, review_status, sha256,
		       (SELECT count(*) - 1 FROM driver_documents h
		         WHERE h.driver_id = d.driver_id AND h.document_type = d.document_type) AS prior_versions
		FROM driver_documents d
		WHERE driver_id = $1 AND superseded_at IS NULL
		ORDER BY uploaded_at DESC
	`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var docType, fileURL, reviewStatus string
		var sha *string
		var priorVersions int
		var uploadedAt time.Time
		if err := rows.Scan(&docType, &fileURL, &uploadedAt, &reviewStatus, &sha, &priorVersions); err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"document_type":  docType,
			"file_url":       fileURL,
			"uploaded_at":    uploadedAt,
			"review_status":  reviewStatus,
			"sha256":         sha,
			"prior_versions": priorVersions,
		})
	}
	return result, nil
}

// resolveVehicleForDocument decides which vehicle a vehicle-level document
// belongs to.
//
// Explicit wins, and must belong to this driver. Otherwise we take the driver's
// active vehicle, then any vehicle. If they have none, one is created from the
// legacy fields on driver_profiles: admin registration writes vehicle_plate and
// transport_type onto the PROFILE and never created a driver_vehicles row, so
// admin-registered drivers had no vehicle to attach paperwork to at all — while
// the self-service apply path has always mirrored one via CreateVehicleFromApply.
// That gap is why per-vehicle documents needed backfilling here rather than just
// a new column.
func (s *Service) resolveVehicleForDocument(ctx context.Context, profileID string, requested *string) (*string, error) {
	if requested != nil && *requested != "" {
		var owned bool
		if err := s.db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM driver_vehicles WHERE id = $1 AND driver_id = $2)`,
			*requested, profileID).Scan(&owned); err != nil {
			return nil, err
		}
		if !owned {
			return nil, apperrors.New(http.StatusBadRequest, "VALIDATION", "vehicle does not belong to this driver")
		}
		return requested, nil
	}

	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM driver_vehicles
		 WHERE driver_id = $1
		 ORDER BY is_active DESC, created_at ASC
		 LIMIT 1
	`, profileID).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// No vehicle row at all — build one from the profile's legacy fields.
	err = s.db.QueryRow(ctx, `
		INSERT INTO driver_vehicles (
			driver_id, vehicle_type_id, plate_number,
			passenger_seats, load_capacity_kg, is_active
		)
		SELECT dp.id, vt.id, dp.vehicle_plate,
		       dp.passenger_seats, dp.load_capacity_kg, TRUE
		  FROM driver_profiles dp
		  JOIN vehicle_types vt ON vt.code = dp.transport_type
		 WHERE dp.id = $1
		RETURNING id
	`, profileID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// transport_type matched no vehicle_types row, or the plate is missing.
			return nil, apperrors.New(http.StatusConflict, "NO_VEHICLE",
				"this driver has no vehicle on record — add a vehicle before attaching vehicle documents")
		}
		if isUniqueViolation(err) {
			// Plate already registered to someone else; do not silently reassign it.
			return nil, apperrors.New(http.StatusConflict, "DUPLICATE_PLATE",
				"the plate on this driver's profile is already registered to another vehicle")
		}
		return nil, err
	}
	s.log.Info().Str("driver_profile_id", profileID).Str("vehicle_id", id).
		Msg("admin: created a driver_vehicles row from legacy profile fields so vehicle documents can attach")
	return &id, nil
}

// UpsertDriverDocument stores an admin-supplied document for a driver.
//
// vehicleID is optional. Vehicle-level types (insurance, authorization) must end
// up attached to a vehicle — enforced by driver_documents_type_scope_chk — so
// when the caller omits it we resolve the driver's vehicle. That keeps every
// existing admin client working, which matters because admin registration is how
// most drivers on this platform were created and none of those calls send it.
func (s *Service) UpsertDriverDocument(ctx context.Context, profileID, documentType, fileURL string, vehicleID *string) error {
	documentType = documents.Normalize(documentType)
	if !documents.IsValid(documentType) {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "unsupported document_type")
	}
	if fileURL == "" {
		return apperrors.ErrBadRequest
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE id = $1)`, profileID).Scan(&exists); err != nil || !exists {
		return apperrors.ErrNotFound
	}

	if documents.RequiresVehicle(documentType) {
		resolved, err := s.resolveVehicleForDocument(ctx, profileID, vehicleID)
		if err != nil {
			return err
		}
		vehicleID = resolved
	} else {
		// Person-level documents must carry no vehicle, or the CHECK rejects them.
		vehicleID = nil
	}
	// Append-only, matching the driver-side path: supersede the live version and
	// insert a new one rather than overwriting file_url in place. The old
	// ON CONFLICT DO UPDATE destroyed whichever file had been approved, so the
	// approval record pointed at bytes that no longer existed.
	//
	// An admin may replace an approved document without a re-upload request —
	// they are the party who would grant one. The replacement still starts
	// PENDING, because a file nobody has looked at is not approved.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// IS NOT DISTINCT FROM, not `=`: vehicle_id is NULL for person-level documents
	// and `NULL = NULL` yields NULL, so `=` would supersede nothing and the insert
	// would collide with the partial unique index instead.
	if _, err := tx.Exec(ctx, `
		UPDATE driver_documents SET superseded_at = NOW()
		 WHERE driver_id = $1 AND document_type = $2
		   AND vehicle_id IS NOT DISTINCT FROM $3
		   AND superseded_at IS NULL
	`, profileID, documentType, vehicleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, document_type, file_url, vehicle_id, review_status)
		VALUES ($1, $2, $3, $4, 'PENDING')
	`, profileID, documentType, fileURL, vehicleID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
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
	// Gender is OPTIONAL (mirrors the driver's own self-registration field,
	// internal/driver ApplyInput.Gender / migration 055) — admin-created
	// drivers previously had no way to record it at all.
	Gender string
	// NationalIDNumber/NationalIDCountry are mandatory only when
	// NATIONAL_ID_REQUIRED is on (DB-1 staged rollout, config.go).
	// CreateDriverFromAdmin sets approval_status = 'APPROVED' directly — it
	// never goes through ApproveDriver, so that function's defensive gate
	// cannot see this path — so the same required-for-approval rule has to be
	// enforced here too (when the flag is on), or admin registration would
	// remain a way to approve a driver with no national ID on file, defeating
	// the uniqueness guard (ban-evasion / bonus-farming) entirely. Normalized
	// + format-validated (pkg/nationalid) whenever a value IS supplied,
	// regardless of the flag — see resolveNationalIDInput.
	NationalIDNumber  string
	NationalIDCountry string
}

// resolveNationalIDInput normalizes and format-validates a national ID
// against the platform's NATIONAL_ID_REQUIRED rollout flag
// (config.DriverConfig.NationalIDRequired, DB-1 staged rollout) — the
// admin-side counterpart of internal/driver's identically-named function
// (kept as a separate small copy, matching this package's existing pattern
// of NOT sharing isNationalIDConflict/isUniqueViolation with internal/driver
// either — the two bounded contexts stay independently editable).
//
// Capture/validation stays active whenever BOTH fields are present,
// regardless of the flag; only whether a value must be present at all is
// gated: required=false + a missing/partial pair returns empty values with no
// error ("not supplied"), required=true is byte-for-byte the original DB-1
// round 2 behaviour.
func resolveNationalIDInput(required bool, country, number string) (normCountry, normNumber string, err error) {
	if country == "" || number == "" {
		if required {
			return "", "", apperrors.New(http.StatusBadRequest, "NATIONAL_ID_REQUIRED",
				"national_id_number and national_id_country are required")
		}
		return "", "", nil
	}
	normCountry, normNumber = nationalid.Normalize(country, number)
	if verr := nationalid.Validate(normCountry, normNumber); verr != nil {
		return "", "", apperrors.New(http.StatusBadRequest, "INVALID_NATIONAL_ID", verr.Error())
	}
	return normCountry, normNumber, nil
}

// Document types now come from pkg/documents, which also records whether each
// one belongs to a person or a vehicle. This map used to be the admin-side copy
// and it disagreed with the driver-side `oneof` tag — admin accepted
// PROFILE_SELFIE and the two *_BACK vehicle types that the driver API rejected,
// so the same document had different names depending on who uploaded it and
// neither side could reliably find the other's rows.

// CreateDriverFromAdmin registers a new driver (user + profile) from the admin panel.
// If a user with the phone already exists, reuse their account.
func (s *Service) CreateDriverFromAdmin(ctx context.Context, in AdminCreateDriverInput) (map[string]interface{}, error) {
	// National ID is mandatory here only when NATIONAL_ID_REQUIRED is on (DB-1
	// staged rollout — see the doc comment on AdminCreateDriverInput). Whether
	// required or not, normalize + format-validate BEFORE any DB write
	// whenever a value IS supplied, same as the driver-onboarding path.
	natCountry, natNumber, err := resolveNationalIDInput(s.nationalIDRequired(), in.NationalIDCountry, in.NationalIDNumber)
	if err != nil {
		return nil, err
	}

	dob := in.DateOfBirth
	if dob == "" {
		dob = "1990-01-01"
	}
	city := in.City
	if city == "" {
		city = "Kigali"
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
	// Gender: normalize "" -> NULL before the INSERT (same normalization the
	// rider path applies in internal/customer.Handler.UpdateProfile). Written
	// literally today, "" only "works" because driver_profiles.gender has no
	// CHECK constraint yet; storing NULL instead matches how riders record
	// "no gender on file" and won't break the day a CHECK is added here too.
	var gender *string
	if in.Gender != "" {
		gender = &in.Gender
	}

	// User-create/promote, the driver_profiles insert, and the national-ID
	// capture all happen in ONE transaction (DB-1 round 2 atomicity fix): a
	// failure at any later step (e.g. a duplicate plate on the driver_profiles
	// insert) rolls back everything, including the national-ID capture — so a
	// phantom user can never end up permanently bound to a real person's ID
	// with no driver record to show for it.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Find or create the user record. National ID capture is deferred until
	// AFTER the driver_profiles insert below succeeds, regardless of whether
	// the user is new or existing — one code path is responsible for it
	// either way (see step 3).
	var userID string
	var existingNationalIDNumber *string
	err = tx.QueryRow(ctx, `SELECT id, national_id_number FROM users WHERE phone_number = $1`, in.Phone).
		Scan(&userID, &existingNationalIDNumber)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing user: %w", err)
	}

	// The step-3 capture below is intentionally first-write-wins (WHERE
	// national_id_number IS NULL) — matching the self-service/resubmission
	// rule. But for an EXISTING account that already has a DIFFERENT national
	// ID on file, that WHERE clause matches zero rows and the write silently
	// no-ops while the rest of the registration still succeeds — the admin's
	// input is dropped on the floor with no signal. Catch that here, before
	// touching this user's row at all (or creating any driver_profiles row),
	// and send the admin to the audited edit path instead of letting the
	// input disappear. Only applies when this call actually supplied a
	// national ID (natNumber != "") — a call that didn't (flag off) has
	// nothing to conflict with, and must not be blocked by whatever the
	// existing account already has on file.
	if natNumber != "" && existingNationalIDNumber != nil && *existingNationalIDNumber != "" && *existingNationalIDNumber != natNumber {
		return nil, errNationalIDMismatchOnAdminCreate()
	}

	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (phone_number, full_name, role_state)
			VALUES ($1, $2, 'DRIVER_ACTIVE')
			RETURNING id`,
			in.Phone, in.FullName,
		).Scan(&userID); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	} else {
		// User exists — promote to DRIVER_ACTIVE.
		if _, err := tx.Exec(ctx,
			`UPDATE users SET role_state = 'DRIVER_ACTIVE', updated_at = NOW() WHERE id = $1`, userID,
		); err != nil {
			return nil, fmt.Errorf("promote existing user: %w", err)
		}
	}

	var existingProfileID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM driver_profiles WHERE user_id = $1`, userID,
	).Scan(&existingProfileID); err == nil {
		return nil, apperrors.Newf(http.StatusConflict, "DRIVER_ALREADY_EXISTS",
			"This phone number already has a driver registration")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing driver profile: %w", err)
	}

	// 2. Create the driver profile — admin registration is pre-approved.
	var profileID string
	err = tx.QueryRow(ctx, `
		INSERT INTO driver_profiles (
			user_id, transport_type, vehicle_plate, license_number,
			date_of_birth, city, momo_provider, momo_pay_code, merchant_pay_code,
			approval_status, approved_by, approved_at,
			province, district, sector, cell, village,
			passenger_seats, load_capacity_kg, gender
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,'APPROVED',$10,NOW(),$11,$12,$13,$14,$15,$16,$17,$18
		) RETURNING id`,
		userID, in.TransportType, in.VehiclePlate, in.LicenseNumber,
		dob, city, momoProvider, momoCode, merchantCode,
		in.AdminUserID,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg, gender,
	).Scan(&profileID)
	if err != nil {
		return nil, mapAdminCreateDriverError(err, in)
	}

	// 3. National ID capture — the LAST write in the transaction, after the
	// profile insert has already succeeded. First-write-wins for an account
	// that somehow already has one captured (matches the self-service and
	// resubmission rule); a brand-new user has nothing to collide with. Only
	// runs when this call actually supplied a national ID — with the flag off
	// and nothing supplied, there is nothing to capture.
	if natNumber != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE users SET national_id_number = $1, national_id_country = $2, updated_at = NOW()
			WHERE id = $3 AND national_id_number IS NULL
		`, natNumber, natCountry, userID); err != nil {
			if isNationalIDConflict(err) {
				return nil, errNationalIDAlreadyRegistered()
			}
			return nil, fmt.Errorf("capture national id: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit driver registration: %w", err)
	}

	// Profile image + documents remain best-effort, OUTSIDE the transaction —
	// the driver record and national ID are already durably committed at this
	// point, so a failure here cannot strand an ID-bound phantom account; it
	// can only leave a real, existing driver missing a photo or a document
	// row, which the existing warning below already surfaces as actionable.
	if in.ProfileImageURL != "" {
		_, _ = s.db.Exec(ctx,
			`UPDATE users SET profile_image_url = $1, updated_at = NOW() WHERE id = $2`,
			in.ProfileImageURL, userID)
	}

	// Attach documents — mirrors mobile step 2 (license, insurance, authorization)
	// vehicleID is nil: UpsertDriverDocument resolves it for vehicle-level types,
	// creating the driver_vehicles row from this profile's plate on the first one.
	for _, doc := range in.Documents {
		if err := s.UpsertDriverDocument(ctx, profileID, doc.DocumentType, doc.FileURL, nil); err != nil {
			// Logged, not fatal — the driver record itself is already committed and
			// failing here would leave a registered driver with no way to retry. But
			// a dropped KYC document must be visible, since the driver is created
			// pre-APPROVED and would otherwise be approved on missing paperwork.
			s.log.Error().Err(err).
				Str("driver_profile_id", profileID).
				Str("document_type", doc.DocumentType).
				Msg("admin: driver registered but a document could not be attached — driver is approved on incomplete papers")
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
	case strings.Contains(msg, "uq_users_national_id"):
		// Defense in depth: the national ID write actually happens AFTER this
		// driver_profiles insert (DB-1 round 2 — deferred capture for
		// atomicity), so this branch should be unreachable in practice — kept
		// in case that ordering ever changes.
		return errNationalIDAlreadyRegistered()
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
