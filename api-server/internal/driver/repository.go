package driver

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
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
}

// Document is a driver_documents row.
type Document struct {
	ID           string    `json:"id"`
	DocumentType string    `json:"document_type"`
	FileURL      string    `json:"file_url"`
	UploadedAt   time.Time `json:"uploaded_at"`
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
	dp.created_at, dp.updated_at
`

func scanProfile(row pgx.Row) (*Profile, error) {
	p := &Profile{}
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
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
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
	FullName      string
	Phone         string
	VehiclePlate  string
	TransportType string
	Lat           float64
	Lng           float64
}

func (r *Repository) GetMatchNotificationInfo(ctx context.Context, profileID string) (*MatchNotificationInfo, error) {
	info := &MatchNotificationInfo{}
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(u.full_name, 'Driver'),
		       COALESCE(u.phone_number, ''),
		       COALESCE(dp.vehicle_plate, ''),
		       dp.transport_type,
		       COALESCE(ST_Y(dl.location::geometry), 0),
		       COALESCE(ST_X(dl.location::geometry), 0)
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

func (r *Repository) CreateProfile(ctx context.Context, in ApplyInput) (*Profile, error) {
	var id string
	err := r.db.QueryRow(ctx, `
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
	return r.FindProfileByUserID(ctx, in.UserID)
}

func (r *Repository) UpdateProfileFields(ctx context.Context, profileID string, city, momoPayCode, momoProvider, fcmToken *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_profiles
		SET city          = COALESCE($1, city),
		    momo_pay_code = COALESCE($2, momo_pay_code),
		    momo_provider = COALESCE($3, momo_provider),
		    updated_at    = NOW()
		WHERE id = $4
	`, city, momoPayCode, momoProvider, profileID)
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

func (r *Repository) UpsertDocument(ctx context.Context, driverProfileID, documentType, fileURL string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO driver_documents (driver_id, document_type, file_url)
		VALUES ($1, $2, $3)
		ON CONFLICT (driver_id, document_type)
		DO UPDATE SET file_url = EXCLUDED.file_url, uploaded_at = NOW()
	`, driverProfileID, documentType, fileURL)
	return err
}

func (r *Repository) ListDocuments(ctx context.Context, driverProfileID string) ([]*Document, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, document_type, file_url, uploaded_at
		FROM driver_documents WHERE driver_id = $1
		ORDER BY uploaded_at ASC
	`, driverProfileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*Document
	for rows.Next() {
		d := &Document{}
		if err := rows.Scan(&d.ID, &d.DocumentType, &d.FileURL, &d.UploadedAt); err != nil {
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
		  AND created_at >= NOW() - ($1 || ' minutes')::INTERVAL`
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
		LIMIT 5
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

// GetEarnings returns the gross fare total AND the number of completed rides for
// the driver within the interval (e.g. "1 day", "7 days").
func (r *Repository) GetEarnings(ctx context.Context, driverUserID string, interval string) (float64, int, error) {
	var total float64
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(r.agreed_fare), 0), COUNT(*)
		FROM rides r
		JOIN driver_profiles dp ON dp.id = r.driver_id
		WHERE dp.user_id = $1
		  AND r.status = 'COMPLETED'
		  AND r.completed_at >= NOW() - ($2 || '')::INTERVAL
	`, driverUserID, interval).Scan(&total, &count)
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

func (r *Repository) UpdateProfileForResubmission(ctx context.Context, in ApplyInput) error {
	_, err := r.db.Exec(ctx, `
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
		    approval_status = 'PENDING_REVIEW',
		    rejection_reason = NULL,
		    updated_at = NOW()
		WHERE user_id = $18
	`,
		in.TransportType, in.VehiclePlate, in.LicenseNumber, in.DateOfBirth,
		in.City, in.MomoPayCode, in.MomoProvider,
		in.Province, in.District, in.Sector, in.Cell, in.Village,
		in.PassengerSeats, in.LoadCapacityKg,
		in.LicenseExpiryDate, in.InsuranceExpiryDate, in.AuthorizationExpiryDate,
		in.UserID,
	)
	return err
}
