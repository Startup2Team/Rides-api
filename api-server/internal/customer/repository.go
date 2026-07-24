package customer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Profile is the customer-facing user profile.
type Profile struct {
	ID                    string  `json:"id"`
	PhoneNumber           string  `json:"phone_number"`
	FullName              *string `json:"full_name"`
	Email                 *string `json:"email,omitempty"`
	FCMToken              *string `json:"fcm_token,omitempty"`
	RoleState             string  `json:"role_state"`
	ProfileImageURL       *string `json:"profile_image_url,omitempty"`
	EmergencyContactName  *string `json:"emergency_contact_name,omitempty"`
	EmergencyContactPhone *string `json:"emergency_contact_phone,omitempty"`
}

// ProfileUpdate carries the mutable customer profile fields. Nil = leave
// unchanged (the UPDATE COALESCEs each column), so callers send only what the
// user actually edited.
type ProfileUpdate struct {
	FullName              *string
	Email                 *string
	FCMToken              *string
	ProfileImageURL       *string
	EmergencyContactName  *string
	EmergencyContactPhone *string
}

// Repository handles customer DB operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) FindByID(ctx context.Context, userID string) (*Profile, error) {
	p := &Profile{}
	err := r.db.QueryRow(ctx, `
		SELECT id, phone_number, full_name, email, fcm_token, role_state, profile_image_url,
		       emergency_contact_name, emergency_contact_phone
		FROM users WHERE id = $1
	`, userID).Scan(&p.ID, &p.PhoneNumber, &p.FullName, &p.Email, &p.FCMToken, &p.RoleState, &p.ProfileImageURL,
		&p.EmergencyContactName, &p.EmergencyContactPhone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// RideStats returns the customer's lifetime COMPLETED-ride count and the sum of
// agreed fares — the inputs to the gamification level. COALESCE keeps the sum at
// 0 (never NULL) for a customer with no completed rides.
func (r *Repository) RideStats(ctx context.Context, userID string) (completedRides int, totalSpend float64, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(agreed_fare), 0)
		 FROM rides WHERE customer_id = $1 AND status = 'COMPLETED'`,
		userID,
	).Scan(&completedRides, &totalSpend)
	return completedRides, totalSpend, err
}

func (r *Repository) UpdateProfile(ctx context.Context, userID string, u ProfileUpdate) error {
	_, err := r.db.Exec(ctx, `
		UPDATE users
		SET full_name               = COALESCE($1, full_name),
		    email                   = COALESCE($2, email),
		    fcm_token               = COALESCE($3, fcm_token),
		    profile_image_url       = COALESCE($4, profile_image_url),
		    emergency_contact_name  = COALESCE($5, emergency_contact_name),
		    emergency_contact_phone = COALESCE($6, emergency_contact_phone),
		    updated_at              = NOW()
		WHERE id = $7
	`, u.FullName, u.Email, u.FCMToken, u.ProfileImageURL, u.EmergencyContactName, u.EmergencyContactPhone, userID)
	return err
}
