package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// User is the auth-layer view of a platform user.
type User struct {
	ID              string
	PhoneNumber     string
	FullName        *string
	Email           *string
	RoleState       string
	DeviceID        *string
	FCMToken        *string
	IsSuspended     bool
	SuspensionUntil *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// OTPRecord is a single OTP challenge row.
type OTPRecord struct {
	ID          string
	PhoneNumber string
	OTPHash     string
	Purpose     string
	IsUsed      bool
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// Repository handles all auth-related database operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) FindUserByPhone(ctx context.Context, phone string) (*User, error) {
	u := &User{}
	err := r.db.QueryRow(ctx, `
		SELECT id, phone_number, full_name, email, role_state, device_id, fcm_token,
		       is_suspended, suspension_until, created_at, updated_at
		FROM users WHERE phone_number = $1
	`, phone).Scan(
		&u.ID, &u.PhoneNumber, &u.FullName, &u.Email, &u.RoleState, &u.DeviceID,
		&u.FCMToken, &u.IsSuspended, &u.SuspensionUntil, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

func (r *Repository) FindUserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := r.db.QueryRow(ctx, `
		SELECT id, phone_number, full_name, email, role_state, device_id, fcm_token,
		       is_suspended, suspension_until, created_at, updated_at
		FROM users WHERE id = $1
	`, id).Scan(
		&u.ID, &u.PhoneNumber, &u.FullName, &u.Email, &u.RoleState, &u.DeviceID,
		&u.FCMToken, &u.IsSuspended, &u.SuspensionUntil, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

func (r *Repository) CreateUser(ctx context.Context, phone, deviceID, platform string, fullName *string, email *string) (*User, error) {
	u := &User{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO users (phone_number, device_id, full_name, email, role_state)
		VALUES ($1, $2, $3, $4, 'CUSTOMER_ONLY')
		RETURNING id, phone_number, full_name, email, role_state, device_id, fcm_token,
		          is_suspended, suspension_until, created_at, updated_at
	`, phone, deviceID, fullName, email).Scan(
		&u.ID, &u.PhoneNumber, &u.FullName, &u.Email, &u.RoleState, &u.DeviceID,
		&u.FCMToken, &u.IsSuspended, &u.SuspensionUntil, &u.CreatedAt, &u.UpdatedAt,
	)
	return u, err
}

func (r *Repository) UpdateUserDeviceID(ctx context.Context, userID, deviceID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET device_id = $1, updated_at = NOW() WHERE id = $2`,
		deviceID, userID,
	)
	return err
}

// UpdateUserPhone swaps a user's phone number. phone_number is UNIQUE, so a
// concurrent claim of the same number surfaces as a 23505 unique violation —
// the caller maps that to a friendly "already in use" error.
func (r *Repository) UpdateUserPhone(ctx context.Context, userID, newPhone string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET phone_number = $1, updated_at = NOW() WHERE id = $2`,
		newPhone, userID,
	)
	return err
}

func (r *Repository) CreateOTP(ctx context.Context, phone, otpHash, purpose string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO otp_verifications (phone_number, otp_hash, purpose, expires_at)
		VALUES ($1, $2, $3, $4)
	`, phone, otpHash, purpose, expiresAt)
	return err
}

func (r *Repository) FindLatestOTP(ctx context.Context, phone, purpose string) (*OTPRecord, error) {
	o := &OTPRecord{}
	err := r.db.QueryRow(ctx, `
		SELECT id, phone_number, otp_hash, purpose, is_used, expires_at, created_at
		FROM otp_verifications
		WHERE phone_number = $1
		  AND purpose = $2
		  AND is_used = FALSE
		  AND expires_at > NOW()
		ORDER BY created_at DESC
		LIMIT 1
	`, phone, purpose).Scan(
		&o.ID, &o.PhoneNumber, &o.OTPHash, &o.Purpose,
		&o.IsUsed, &o.ExpiresAt, &o.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrInvalidOTP
		}
		return nil, err
	}
	return o, nil
}

func (r *Repository) MarkOTPUsed(ctx context.Context, otpID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE otp_verifications SET is_used = TRUE WHERE id = $1`, otpID,
	)
	return err
}

func (r *Repository) LogDeviceSession(ctx context.Context, userID, deviceID, platform, appVersion, ipAddr string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO device_sessions (user_id, device_id, platform, app_version, ip_address)
		VALUES ($1, $2, $3, $4, $5::INET)
	`, userID, deviceID, platform, appVersion, ipAddr)
	return err
}

func (r *Repository) DetectDeviceCollision(ctx context.Context, deviceID, userID string) (bool, error) {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT user_id)
		FROM device_sessions
		WHERE device_id = $1 AND user_id != $2
	`, deviceID, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) FlagUserForReview(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET is_suspended = TRUE, updated_at = NOW() WHERE id = $1`, userID,
	)
	return err
}

// ClearSuspension lifts a suspension (used to auto-expire temporary bans).
func (r *Repository) ClearSuspension(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET is_suspended = FALSE, suspension_until = NULL, updated_at = NOW() WHERE id = $1`, userID,
	)
	return err
}

// AnonymizeUser scrubs all PII from users and associated tables within a transaction.
func (r *Repository) AnonymizeUser(ctx context.Context, userID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Delete device sessions
	_, err = tx.Exec(ctx, `DELETE FROM device_sessions WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}

	// 2. Delete saved locations
	_, err = tx.Exec(ctx, `DELETE FROM saved_locations WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}

	// 3. Delete driver locations
	_, err = tx.Exec(ctx, `DELETE FROM driver_locations WHERE driver_id IN (SELECT id FROM driver_profiles WHERE user_id = $1)`, userID)
	if err != nil {
		return err
	}

	// 4. Delete driver documents
	_, err = tx.Exec(ctx, `DELETE FROM driver_documents WHERE driver_id IN (SELECT id FROM driver_profiles WHERE user_id = $1)`, userID)
	if err != nil {
		return err
	}

	// 5. Update driver vehicles to scrub and release plate numbers
	_, err = tx.Exec(ctx, `
		UPDATE driver_vehicles
		SET plate_number = 'del_pl_' || left(id::text, 12),
			is_active = FALSE,
			updated_at = NOW()
		WHERE driver_id IN (SELECT id FROM driver_profiles WHERE user_id = $1)
	`, userID)
	if err != nil {
		return err
	}

	// 6. Update driver profile to scrub and release license numbers/sensitive info
	_, err = tx.Exec(ctx, `
		UPDATE driver_profiles
		SET vehicle_plate = 'del_pl_' || left(id::text, 12),
			license_number = 'del_li_' || left(id::text, 12),
			city = 'deleted',
			momo_pay_code = 'deleted',
			province = NULL,
			district = NULL,
			sector = NULL,
			cell = NULL,
			village = NULL,
			is_online = FALSE,
			updated_at = NOW()
		WHERE user_id = $1
	`, userID)
	if err != nil {
		return err
	}

	// 7. Update user profile to scrub personal details and release phone number
	_, err = tx.Exec(ctx, `
		UPDATE users
		SET phone_number = 'del_' || left(id::text, 15),
			full_name = 'Deleted User',
			email = NULL,
			device_id = NULL,
			fcm_token = NULL,
			updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
