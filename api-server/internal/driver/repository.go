package driver

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/nationalid"
)

// Profile is the driver_profiles view.
type Profile struct {
	ID                      string     `json:"id"`
	UserID                  string     `json:"user_id"`
	TransportType           string     `json:"transport_type"`
	VehiclePlate            string     `json:"vehicle_plate"`
	LicenseNumber           string     `json:"license_number"`
	DateOfBirth             time.Time  `json:"date_of_birth"`
	City                    string     `json:"city"`
	MomoPayCode             string     `json:"momo_pay_code"`
	MomoProvider            string     `json:"momo_provider"`
	Province                string     `json:"province"`
	District                string     `json:"district"`
	Sector                  string     `json:"sector"`
	Cell                    string     `json:"cell"`
	Village                 string     `json:"village"`
	Gender                  string     `json:"gender,omitempty"`
	PassengerSeats          *int       `json:"passenger_seats,omitempty"`
	LoadCapacityKg          *int       `json:"load_capacity_kg,omitempty"`
	ApprovalStatus          string     `json:"approval_status"`
	ApprovedBy              *string    `json:"approved_by,omitempty"`
	ApprovedAt              *time.Time `json:"approved_at,omitempty"`
	RejectionReason         *string    `json:"rejection_reason,omitempty"`
	SuspensionReason        *string    `json:"suspension_reason,omitempty"`
	IsOnline                bool       `json:"is_online"`
	PriorityTier            int        `json:"priority_tier"`
	OfflineAt               *time.Time `json:"offline_at,omitempty"`
	AcceptanceRate          float64    `json:"acceptance_rate"`
	TotalRides              int        `json:"total_rides"`
	PolicyAccepted          bool       `json:"policy_accepted"`
	FCMToken                *string    `json:"fcm_token,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	LicenseExpiryDate       *time.Time `json:"license_expiry_date,omitempty"`
	InsuranceExpiryDate     *time.Time `json:"insurance_expiry_date,omitempty"`
	AuthorizationExpiryDate *time.Time `json:"authorization_expiry_date,omitempty"`
	// NationalIDNumber is MASKED (last 4 characters only) even in the driver's
	// own profile — the unredacted number is exposed nowhere except the admin
	// driver-detail payload (internal/admin GetDriver). See pkg/nationalid.Mask.
	// DB-1: flagged for senior-security review (own-profile masking vs full).
	NationalIDNumber  *string `json:"national_id_number,omitempty"`
	NationalIDCountry *string `json:"national_id_country,omitempty"`
}

// Document is a driver_documents row.
type Document struct {
	ID           string    `json:"id"`
	DocumentType string    `json:"document_type"`
	FileURL      string    `json:"file_url"`
	UploadedAt   time.Time `json:"uploaded_at"`
	// ReviewStatus is per-document, independent of the driver's overall
	// approval: PENDING | APPROVED | REJECTED.
	ReviewStatus string `json:"review_status"`
	// Editable tells the app whether a replacement would be accepted. Approved
	// documents are view-only unless an admin has opened a re-upload window, so
	// the app can render the correct affordance instead of offering a button the
	// API will reject.
	Editable bool    `json:"editable"`
	SHA256   *string `json:"sha256,omitempty"`
	// VehicleID is set for vehicle-level documents (insurance, authorization) and
	// nil for person-level ones (licence, ID, selfie). The app groups by this to
	// show each vehicle its own paperwork; before it existed a driver's second
	// vehicle silently superseded the first vehicle's documents.
	VehicleID *string `json:"vehicle_id,omitempty"`
}

// ErrDocumentLocked is returned when a driver tries to replace a document that
// has already been APPROVED and has no open admin re-upload request.
//
// This is the server-side half of "documents are view-only after approval".
// Hiding the replace button in the app is not enforcement — without this check a
// driver could get approved with genuine papers and then swap them for anything,
// keeping their APPROVED status, because approval lives on driver_profiles and
// nothing bound it to a specific file.
var ErrDocumentLocked = errors.New("document is approved and view-only; ask an admin to request a re-upload")

// ErrNationalIDTaken is returned when the submitted national_id_number is
// already registered to a DIFFERENT account (the uq_users_national_id partial
// unique index, Postgres 23505). This is the one-ID-one-account fraud guard
// (ban evasion / registration-bonus farming) surfacing as a friendly error
// instead of a raw constraint violation.
var ErrNationalIDTaken = errors.New("this national ID is already registered to another account")

// isNationalIDConflict reports whether err is specifically a 23505 violation
// of uq_users_national_id — as opposed to any other unique constraint (vehicle
// plate, license number) that a driver_profiles write might also hit. Matching
// on the constraint name (not a message substring) is what lets the two be
// told apart and mapped to different client-facing errors.
func isNationalIDConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_users_national_id"
}

// setUserNationalIDTx captures a national ID onto users within an existing
// transaction, but ONLY when the caller supplied one (empty country/number is
// the additive "not provided" case — old app versions and admin flows that
// omit the field must behave exactly as before) AND only when this user does
// not already have one on file.
//
// The "already have one" guard is what makes this the driver-onboarding path
// rather than the admin-edit path: a driver applying/resubmitting can never
// overwrite a previously captured ID this way (silently a no-op instead of an
// error, since resubmitting the same value they already have on file is the
// common case and shouldn't fail). Correcting an existing value is
// admin-only — see internal/admin SetDriverNationalID, which updates
// unconditionally.
func setUserNationalIDTx(ctx context.Context, tx pgx.Tx, userID, country, number string) error {
	if number == "" || country == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE users
		SET national_id_number = $1, national_id_country = $2, updated_at = NOW()
		WHERE id = $3 AND national_id_number IS NULL
	`, number, country, userID)
	if err != nil {
		if isNationalIDConflict(err) {
			return ErrNationalIDTaken
		}
		return err
	}
	return nil
}

// NearbyDriver is the anonymised view returned to customers.
type NearbyDriver struct {
	TransportType string  `json:"transport_type"`
	DistanceM     float64 `json:"distance_m"`
	ApproxLat     float64 `json:"approx_lat"`
	ApproxLng     float64 `json:"approx_lng"`
	ETAMinutes    int     `json:"eta_minutes"`
}

// NearbyCandidate is used internally by the matching engine.
type NearbyCandidate struct {
	ProfileID      string
	UserID         string
	TransportType  string
	PriorityTier   int
	FCMToken       *string
	DistanceM      float64
	AcceptanceRate float64
	Lat            float64
	Lng            float64
}

// Repository handles driver DB operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const profileSelectCols = `
	dp.id, dp.user_id, dp.transport_type, dp.vehicle_plate, dp.license_number,
	dp.date_of_birth, dp.city, dp.momo_pay_code,
	COALESCE(dp.momo_provider, ''),
	COALESCE(dp.province, ''), COALESCE(dp.district, ''), COALESCE(dp.sector, ''),
	COALESCE(dp.cell, ''), COALESCE(dp.village, ''),
	COALESCE(dp.gender, ''),
	dp.passenger_seats, dp.load_capacity_kg,
	dp.approval_status, dp.approved_by, dp.approved_at,
	dp.rejection_reason, dp.suspension_reason,
	dp.is_online, dp.priority_tier, dp.offline_at,
	dp.acceptance_rate, dp.total_rides,
	COALESCE(dp.policy_accepted, FALSE),
	u.fcm_token,
	dp.license_expiry_date, dp.insurance_expiry_date, dp.authorization_expiry_date,
	dp.created_at, dp.updated_at,
	u.national_id_number, u.national_id_country
`

func scanProfile(row pgx.Row) (*Profile, error) {
	p := &Profile{}
	var rawNationalID *string
	err := row.Scan(
		&p.ID, &p.UserID, &p.TransportType, &p.VehiclePlate, &p.LicenseNumber,
		&p.DateOfBirth, &p.City, &p.MomoPayCode,
		&p.MomoProvider,
		&p.Province, &p.District, &p.Sector, &p.Cell, &p.Village,
		&p.Gender,
		&p.PassengerSeats, &p.LoadCapacityKg,
		&p.ApprovalStatus, &p.ApprovedBy, &p.ApprovedAt,
		&p.RejectionReason, &p.SuspensionReason,
		&p.IsOnline, &p.PriorityTier, &p.OfflineAt,
		&p.AcceptanceRate, &p.TotalRides,
		&p.PolicyAccepted,
		&p.FCMToken,
		&p.LicenseExpiryDate, &p.InsuranceExpiryDate, &p.AuthorizationExpiryDate,
		&p.CreatedAt, &p.UpdatedAt,
		&rawNationalID, &p.NationalIDCountry,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	// Mask at the point of exposure so the raw number never travels further
	// than this scan, in either direction (own profile or session bootstrap).
	if rawNationalID != nil {
		masked := nationalid.Mask(*rawNationalID)
		p.NationalIDNumber = &masked
	}
	return p, nil
}

func (r *Repository) FindProfileByID(ctx context.Context, profileID string) (*Profile, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+profileSelectCols+`
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		WHERE dp.id = $1
	`, profileID)
	return scanProfile(row)
}

// MatchNotificationInfo is sent to the customer when a driver accepts a ride request.
type MatchNotificationInfo struct {
	FullName        string
	Phone           string
	VehiclePlate    string
	TransportType   string
	Lat             float64
	Lng             float64
	Rating          float64
	ProfileImageURL string
}

func (r *Repository) GetMatchNotificationInfo(ctx context.Context, profileID string) (*MatchNotificationInfo, error) {
	info := &MatchNotificationInfo{}
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(u.full_name, 'Driver'),
		       COALESCE(u.phone_number, ''),
		       COALESCE(dp.vehicle_plate, ''),
		       dp.transport_type,
		       COALESCE(ST_Y(dl.location::geometry), 0),
		       COALESCE(ST_X(dl.location::geometry), 0),
		       COALESCE(dp.rating, 5),
		       COALESCE(u.profile_image_url, '')
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		LEFT JOIN driver_locations dl ON dl.driver_id = dp.id
		WHERE dp.id = $1
	`, profileID).Scan(
		&info.FullName,
		&info.Phone,
		&info.VehiclePlate,
		&info.TransportType,
		&info.Lat,
		&info.Lng,
		&info.Rating,
		&info.ProfileImageURL,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return info, nil
}

func (r *Repository) FindProfileByUserID(ctx context.Context, userID string) (*Profile, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+profileSelectCols+`
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		WHERE dp.user_id = $1
	`, userID)
	return scanProfile(row)
}

// CreateProfile inserts a new driver_profiles row and, when the applicant
// supplied a national ID, captures it onto users in the SAME transaction.
//
// The national ID column lives on users (not driver_profiles) because it is
// an attribute of the person, who already has a users row from auth — so this
// can never be a separate, non-atomic write: either both the application and
// the ID capture land, or neither does.
func (r *Repository) CreateProfile(ctx context.Context, in ApplyInput) (*Profile, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO driver_profiles (
			user_id, transport_type, vehicle_plate, license_number, date_of_birth,
			city, momo_pay_code, momo_provider,
			province, district, sector, cell, village,
			passenger_seats, load_capacity_kg,
			license_expiry_date, insurance_expiry_date, authorization_expiry_date,
			gender
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id
	`,
		in.UserID, in.TransportType, in.VehiclePlate, in.LicenseNumber, in.DateOfBirth,
		in.City, in.MomoPayCode, in.MomoProvider,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg,
		in.LicenseExpiryDate, in.InsuranceExpiryDate, in.AuthorizationExpiryDate,
		in.Gender,
	).Scan(&id)
	if err != nil {
		return nil, err
	}

	if err := setUserNationalIDTx(ctx, tx, in.UserID, in.NationalIDCountry, in.NationalIDNumber); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.FindProfileByUserID(ctx, in.UserID)
}

func (r *Repository) UpdateProfileFields(ctx context.Context, profileID string, city, momoPayCode, momoProvider, gender, fcmToken *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_profiles
		SET city          = COALESCE($1, city),
		    momo_pay_code = COALESCE($2, momo_pay_code),
		    momo_provider = COALESCE($3, momo_provider),
		    gender        = COALESCE($4, gender),
		    updated_at    = NOW()
		WHERE id = $5
	`, city, momoPayCode, momoProvider, gender, profileID)
	if err != nil {
		return err
	}
	// fcm_token lives on users table
	if fcmToken != nil {
		_, err = r.db.Exec(ctx,
			`UPDATE users SET fcm_token = $1, updated_at = NOW()
			 WHERE id = (SELECT user_id FROM driver_profiles WHERE id = $2)`,
			fcmToken, profileID)
	}
	return err
}

func (r *Repository) SetPolicyAccepted(ctx context.Context, profileID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_profiles
		SET policy_accepted = TRUE, policy_accepted_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, profileID)
	return err
}

// UpsertDocument records a new version of a document, append-only.
//
// It used to be `ON CONFLICT ... DO UPDATE SET file_url`, which overwrote the row
// and destroyed the previously approved file. Now the live row is marked
// superseded and a new row inserted, so the chain is preserved and "which file
// did we approve" stays answerable.
//
// asAdmin distinguishes the two upload paths. A driver may not replace an
// APPROVED document unless an admin has opened a re-upload window; an admin
// acting on the driver's behalf always may, since they are the ones who would
// have opened that window. Both run in one transaction: superseding without
// inserting would leave the driver with no live document at all.
// VehicleBelongsToDriver reports whether vehicleID is one of this driver's
// vehicles. Guards vehicle-scoped writes so a driver cannot attach documents to
// a vehicle they do not own. Deleted/inactive vehicles still count as owned —
// is_active governs which vehicle they are driving, not whose paperwork it is.
func (r *Repository) VehicleBelongsToDriver(ctx context.Context, driverProfileID, vehicleID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM driver_vehicles WHERE id = $1 AND driver_id = $2
		)
	`, vehicleID, driverProfileID).Scan(&exists)
	return exists, err
}

// UpsertDocument stores a new version of one document, superseding the live one.
//
// vehicleID scopes the document: nil for person-level types (licence, ID,
// selfie), the owning vehicle for vehicle-level types (insurance,
// authorization). It participates in the uniqueness lookup below, which is what
// lets a driver hold insurance for two vehicles at once — previously the second
// vehicle's upload superseded the first vehicle's, because the live-row query
// matched on (driver_id, document_type) alone.
func (r *Repository) UpsertDocument(ctx context.Context, driverProfileID, documentType, fileURL, sha256 string, vehicleID *string, asAdmin bool) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the live row so two concurrent uploads cannot both supersede it and
	// then both insert, which the partial unique index would reject anyway — but
	// with a confusing constraint error rather than a clean serialisation.
	//
	// `vehicle_id IS NOT DISTINCT FROM $3` rather than `=` because vehicle_id is
	// NULL for person-level documents and `NULL = NULL` is NULL, not true — with
	// `=` the lookup would never find an existing licence row, so every upload
	// would insert a duplicate and hit the unique index instead of superseding.
	var liveID, liveStatus string
	var reuploadOpen bool
	err = tx.QueryRow(ctx, `
		SELECT id, review_status, (reupload_requested_at IS NOT NULL)
		  FROM driver_documents
		 WHERE driver_id = $1 AND document_type = $2
		   AND vehicle_id IS NOT DISTINCT FROM $3
		   AND superseded_at IS NULL
		 FOR UPDATE
	`, driverProfileID, documentType, vehicleID).Scan(&liveID, &liveStatus, &reuploadOpen)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// First upload of this type — nothing to supersede.
	case err != nil:
		return err
	default:
		if liveStatus == "APPROVED" && !reuploadOpen && !asAdmin {
			return ErrDocumentLocked
		}
		if _, err := tx.Exec(ctx, `
			UPDATE driver_documents SET superseded_at = NOW() WHERE id = $1
		`, liveID); err != nil {
			return err
		}
	}

	// The replacement always starts PENDING: a new file has not been reviewed,
	// whatever the status of the one it replaces. This is what re-opens review
	// after an approved driver swaps a document.
	var hash any
	if sha256 != "" {
		hash = sha256
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, document_type, file_url, sha256, vehicle_id, review_status)
		VALUES ($1, $2, $3, $4, $5, 'PENDING')
	`, driverProfileID, documentType, fileURL, hash, vehicleID); err != nil {
		return err
	}

	// Consume the re-upload window so it authorises exactly one replacement.
	if reuploadOpen {
		if _, err := tx.Exec(ctx, `
			UPDATE driver_documents
			   SET reupload_requested_at = NULL, reupload_requested_by = NULL
			 WHERE id = $1
		`, liveID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// RequestDocumentReupload opens a one-shot window letting the driver replace an
// otherwise view-only approved document. Consumed by the next upload.
func (r *Repository) RequestDocumentReupload(ctx context.Context, driverProfileID, documentType, adminID string) error {
	var admin any
	if adminID != "" {
		admin = adminID
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE driver_documents
		   SET reupload_requested_at = NOW(), reupload_requested_by = $3
		 WHERE driver_id = $1 AND document_type = $2 AND superseded_at IS NULL
	`, driverProfileID, documentType, admin)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// SetDocumentReview records a per-document decision against the live version.
// Binding the decision to a row that can no longer change is what makes "this is
// the licence the reviewer approved" checkable rather than arguable.
func (r *Repository) SetDocumentReview(ctx context.Context, documentID, status, adminID, notes string) error {
	var admin any
	if adminID != "" {
		admin = adminID
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE driver_documents
		   SET review_status = $2, reviewed_at = NOW(), reviewed_by = $3, review_notes = NULLIF($4, '')
		 WHERE id = $1 AND superseded_at IS NULL
	`, documentID, status, admin, notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// ListDocuments returns the LIVE version of each document.
//
// Superseded rows are deliberately excluded: the driver's own list should show
// what currently stands, not every historical attempt. Admin review reads the
// full chain separately.
//
// `editable` is computed here rather than left to the app, so the affordance the
// app renders and the rule the API enforces come from one place.
func (r *Repository) ListDocuments(ctx context.Context, driverProfileID string) ([]*Document, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, document_type, file_url, uploaded_at, review_status, sha256, vehicle_id,
		       (review_status <> 'APPROVED' OR reupload_requested_at IS NOT NULL) AS editable
		FROM driver_documents
		WHERE driver_id = $1 AND superseded_at IS NULL
		ORDER BY vehicle_id NULLS FIRST, uploaded_at ASC
	`, driverProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.DocumentType, &d.FileURL, &d.UploadedAt,
			&d.ReviewStatus, &d.SHA256, &d.VehicleID, &d.Editable); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// ListDocumentHistory returns every version of every document, newest first —
// the audit view behind "what did we approve on the 4th".
func (r *Repository) ListDocumentHistory(ctx context.Context, driverProfileID string) ([]*Document, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, document_type, file_url, uploaded_at, review_status, sha256,
		       (superseded_at IS NULL
		        AND (review_status <> 'APPROVED' OR reupload_requested_at IS NOT NULL)) AS editable
		FROM driver_documents
		WHERE driver_id = $1
		ORDER BY document_type ASC, uploaded_at DESC
	`, driverProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.DocumentType, &d.FileURL, &d.UploadedAt,
			&d.ReviewStatus, &d.SHA256, &d.Editable); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (r *Repository) UpdateOnlineStatus(ctx context.Context, userID string, isOnline bool) error {
	if isOnline {
		_, err := r.db.Exec(ctx,
			`UPDATE driver_profiles SET is_online = TRUE, updated_at = NOW() WHERE user_id = $1`, userID)
		return err
	}
	_, err := r.db.Exec(ctx,
		`UPDATE driver_profiles SET is_online = FALSE, offline_at = NOW(), updated_at = NOW() WHERE user_id = $1 AND is_online = TRUE`, userID)
	return err
}

func (r *Repository) UpsertLocation(ctx context.Context, driverProfileID string, loc geo.Point, speedKMH, heading *float64) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO driver_locations (driver_id, location, speed_kmh, heading, updated_at)
		VALUES ($1, ST_GeographyFromText($2), $3, $4, NOW())
		ON CONFLICT (driver_id) DO UPDATE
		SET location   = EXCLUDED.location,
		    speed_kmh  = EXCLUDED.speed_kmh,
		    heading    = EXCLUDED.heading,
		    updated_at = NOW()
	`, driverProfileID, loc.WKT(), speedKMH, heading)
	return err
}

// DemandCell is one bucketed pickup-demand cell for the driver heatmap.
type DemandCell struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Count int     `json:"count"`
}

// DemandHeatmap buckets recent ride pickups onto a ~110 m grid (3-decimal
// rounding) over the last windowMin minutes, busiest cells first. When center
// is non-nil it restricts to radiusM metres around it; otherwise it returns the
// busiest cells platform-wide. Read-only; safe for drivers to poll.
func (r *Repository) DemandHeatmap(ctx context.Context, windowMin int, center *geo.Point, radiusM int) ([]DemandCell, error) {
	q := `
		SELECT ROUND(ST_Y(pickup_point::geometry)::NUMERIC, 3) AS lat_bucket,
		       ROUND(ST_X(pickup_point::geometry)::NUMERIC, 3) AS lng_bucket,
		       COUNT(*) AS demand_count
		FROM rides
		WHERE pickup_point IS NOT NULL
		  AND created_at >= NOW() - make_interval(mins => $1)`
	args := []interface{}{windowMin}
	if center != nil {
		q += `
		  AND ST_DWithin(pickup_point, ST_GeographyFromText($2), $3)`
		args = append(args, center.WKT(), radiusM)
	}
	q += `
		GROUP BY lat_bucket, lng_bucket
		ORDER BY demand_count DESC
		LIMIT 300`

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cells := make([]DemandCell, 0)
	for rows.Next() {
		var c DemandCell
		if err := rows.Scan(&c.Lat, &c.Lng, &c.Count); err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

func (r *Repository) FindNearby(ctx context.Context, loc geo.Point, radiusM int, transportType string, excludedIDs []string) ([]*NearbyCandidate, error) {
	if excludedIDs == nil {
		excludedIDs = []string{}
	}
	rows, err := r.db.Query(ctx, `
		SELECT dp.id, dp.user_id, dp.transport_type, dp.priority_tier, u.fcm_token,
		       ST_Distance(dl.location, ST_GeographyFromText($1)) AS distance_m,
		       dp.acceptance_rate,
		       ST_X(dl.location::geometry) AS lng,
		       ST_Y(dl.location::geometry) AS lat
		FROM driver_locations dl
		JOIN driver_profiles dp ON dp.id = dl.driver_id
		JOIN users u ON u.id = dp.user_id
		WHERE dp.is_online = TRUE
		  AND dp.approval_status = 'APPROVED'
		  AND dp.transport_type = $2
		  -- Location freshness. is_online was the only gate, so a driver who shut
		  -- the app without going offline kept matching against a position that
		  -- could be hours old -- offering rides to someone already at home. The
		  -- Redis path gets this free from the 120s TTL on driver:<id>:location;
		  -- this fallback had no equivalent. Kept generous (5x that TTL) because
		  -- the fallback exists for cold start, when fixes are naturally sparse.
		  AND dl.updated_at > NOW() - INTERVAL '10 minutes'
		  AND ST_DWithin(dl.location, ST_GeographyFromText($1), $3)
		  AND dp.id != ALL($4::uuid[])
		  AND dp.user_id NOT IN (
		      SELECT COALESCE(dp2.user_id, '00000000-0000-0000-0000-000000000000'::UUID)
		      FROM rides r2
		      LEFT JOIN driver_profiles dp2 ON dp2.id = r2.driver_id
		      WHERE r2.status NOT IN ('COMPLETED','CANCELLED')
		      AND r2.driver_id IS NOT NULL
		  )
		ORDER BY dp.priority_tier ASC, distance_m ASC
		-- Raised from 5: tiered batching needs enough of the sorted list to fill
		-- several distance bands.
		LIMIT 30
	`, loc.WKT(), transportType, radiusM, excludedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []*NearbyCandidate
	for rows.Next() {
		c := &NearbyCandidate{}
		if err := rows.Scan(&c.ProfileID, &c.UserID, &c.TransportType, &c.PriorityTier, &c.FCMToken, &c.DistanceM, &c.AcceptanceRate, &c.Lng, &c.Lat); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

func (r *Repository) LogGPSAnomaly(ctx context.Context, driverProfileID string, speed float64, last, newLoc *geo.Point) error {
	var lastWKT, newWKT interface{}
	if last != nil {
		lastWKT = last.WKT()
	}
	if newLoc != nil {
		newWKT = newLoc.WKT()
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO gps_anomalies (driver_id, computed_speed, last_location, new_location)
		VALUES ($1, $2,
			CASE WHEN $3::TEXT IS NULL THEN NULL ELSE ST_GeographyFromText($3::TEXT) END,
			CASE WHEN $4::TEXT IS NULL THEN NULL ELSE ST_GeographyFromText($4::TEXT) END
		)
	`, driverProfileID, speed, lastWKT, newWKT)
	return err
}

func (r *Repository) SetPriorityTier(ctx context.Context, driverProfileID string, tier int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE driver_profiles SET priority_tier = $1, updated_at = NOW() WHERE id = $2`,
		tier, driverProfileID,
	)
	return err
}

func (r *Repository) SetApprovalStatus(ctx context.Context, profileID, status, approvedBy string, rejectionReason *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_profiles
		-- Every $1/$2 use is explicitly ::text so Postgres deduces ONE type per
		-- parameter: mixing an untyped assignment with CASE comparisons made it
		-- deduce inconsistent types and reject the statement at parse time. The
		-- uuid cast runs only in the taken branch (NULLIF keeps '' out — the
		-- dev-auto-approve caller passes no admin id).
		SET approval_status = $1::text,
		    approved_by = CASE WHEN $1::text = 'APPROVED' AND NULLIF($2::text, '') IS NOT NULL THEN NULLIF($2::text, '')::UUID ELSE approved_by END,
		    approved_at = CASE WHEN $1::text = 'APPROVED' THEN NOW() ELSE approved_at END,
		    rejection_reason = $3,
		    updated_at = NOW()
		WHERE id = $4
	`, status, approvedBy, rejectionReason, profileID)
	return err
}

func (r *Repository) UpdateUserRoleState(ctx context.Context, userID, roleState string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET role_state = $1, updated_at = NOW() WHERE id = $2`,
		roleState, userID,
	)
	return err
}

// GetEarnings returns the gross fare total AND the number of completed rides the
// driver finished within the half-open window [start, end).
//
// The window is passed in as explicit instants rather than derived here, because
// "today" is a local-calendar concept: callers build it with timeutil.DayWindow
// in the configured platform timezone. This used to be a rolling
// `completed_at >= NOW() - '1 day'` interval labelled "today", which meant a
// driver's morning total still carried the previous evening's rides and then
// shrank through the day as those rides aged past the 24-hour mark.
//
// The fare column matches the owner digest (internal/digest/repository.go) —
// final_fare_rwf when the fare engine has settled one, otherwise the agreed
// fare. Summing agreed_fare alone made the driver's app and the digest report
// different money for identical rides.
func (r *Repository) GetEarnings(ctx context.Context, driverUserID string, start, end time.Time) (float64, int, error) {
	var total float64
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(r.final_fare_rwf, r.agreed_fare, 0)), 0), COUNT(*)
		FROM rides r
		JOIN driver_profiles dp ON dp.id = r.driver_id
		WHERE dp.user_id = $1
		  AND r.status = 'COMPLETED'
		  AND r.completed_at >= $2
		  AND r.completed_at < $3
	`, driverUserID, start, end).Scan(&total, &count)
	return total, count, err
}

func (r *Repository) GetCompletionRate(ctx context.Context, driverProfileID string) (float64, error) {
	var rate float64
	err := r.db.QueryRow(ctx, `
		SELECT CASE WHEN COUNT(*) = 0 THEN 100.0
		       ELSE ROUND(COUNT(*) FILTER (WHERE status = 'COMPLETED') * 100.0 / COUNT(*), 2)
		       END
		FROM rides WHERE driver_id = $1
	`, driverProfileID).Scan(&rate)
	return rate, err
}

// HasActiveRide returns true if the driver has a ride in a non-terminal state in the DB.
// Used to cross-check a stale Redis driver:active_ride key before blocking offline transitions.
func (r *Repository) HasActiveRide(ctx context.Context, driverUserID string) bool {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides r
		JOIN driver_profiles dp ON dp.id = r.driver_id
		WHERE dp.user_id = $1
		  AND r.status NOT IN ('COMPLETED','CANCELLED')
	`, driverUserID).Scan(&count)
	return err == nil && count > 0
}

// UpdateProfileForResubmission updates a previously-REJECTED profile and, like
// CreateProfile, captures the national ID (if supplied and not already set for
// this user) onto users in the SAME transaction as the driver_profiles update.
func (r *Repository) UpdateProfileForResubmission(ctx context.Context, in ApplyInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET transport_type = $1,
		    vehicle_plate = $2,
		    license_number = $3,
		    date_of_birth = $4,
		    city = $5,
		    momo_pay_code = $6,
		    momo_provider = $7,
		    province = $8,
		    district = $9,
		    sector = $10,
		    cell = $11,
		    village = $12,
		    passenger_seats = $13,
		    load_capacity_kg = $14,
		    license_expiry_date = $15,
		    insurance_expiry_date = $16,
		    authorization_expiry_date = $17,
		    gender = COALESCE(NULLIF($18, ''), gender),
		    approval_status = 'PENDING_REVIEW',
		    rejection_reason = NULL,
		    updated_at = NOW()
		WHERE user_id = $19
	`,
		in.TransportType, in.VehiclePlate, in.LicenseNumber, in.DateOfBirth,
		in.City, in.MomoPayCode, in.MomoProvider,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg,
		in.LicenseExpiryDate, in.InsuranceExpiryDate, in.AuthorizationExpiryDate,
		in.Gender,
		in.UserID,
	)
	if err != nil {
		return err
	}

	if err := setUserNationalIDTx(ctx, tx, in.UserID, in.NationalIDCountry, in.NationalIDNumber); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
