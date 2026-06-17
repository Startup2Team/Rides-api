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
	ID              string  `json:"id"`
	PhoneNumber     string  `json:"phone_number"`
	FullName        *string `json:"full_name"`
	Email           *string `json:"email,omitempty"`
	FCMToken        *string `json:"fcm_token,omitempty"`
	RoleState       string  `json:"role_state"`
	ProfileImageURL *string `json:"profile_image_url,omitempty"`
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
		SELECT id, phone_number, full_name, email, fcm_token, role_state, profile_image_url
		FROM users WHERE id = $1
	`, userID).Scan(&p.ID, &p.PhoneNumber, &p.FullName, &p.Email, &p.FCMToken, &p.RoleState, &p.ProfileImageURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *Repository) UpdateProfile(ctx context.Context, userID string, fullName, email, fcmToken, profileImageURL *string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE users
		SET full_name         = COALESCE($1, full_name),
		    email             = COALESCE($2, email),
		    fcm_token         = COALESCE($3, fcm_token),
		    profile_image_url = COALESCE($4, profile_image_url),
		    updated_at        = NOW()
		WHERE id = $5
	`, fullName, email, fcmToken, profileImageURL, userID)
	return err
}
