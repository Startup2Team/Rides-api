package rating

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Rating struct {
	ID        string    `json:"id"`
	RideID    string    `json:"ride_id"`
	RaterID   string    `json:"rater_id"`
	RatedID   string    `json:"rated_id"`
	Direction string    `json:"direction"`
	Score     int       `json:"score"`
	Comment   *string   `json:"comment,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, rideID, raterID, ratedID, direction string, score int, comment *string, tags []string) (*Rating, error) {
	rat := &Rating{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO ratings (ride_id, rater_id, rated_id, direction, score, comment, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (ride_id, rater_id) DO UPDATE
		SET score = EXCLUDED.score, comment = EXCLUDED.comment, tags = EXCLUDED.tags
		RETURNING id, ride_id, rater_id, rated_id, direction, score, comment, tags, created_at
	`, rideID, raterID, ratedID, direction, score, comment, tags).Scan(
		&rat.ID, &rat.RideID, &rat.RaterID, &rat.RatedID,
		&rat.Direction, &rat.Score, &rat.Comment, &rat.Tags, &rat.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return rat, nil
}

func (r *Repository) FindByRideAndRater(ctx context.Context, rideID, raterID string) (*Rating, error) {
	rat := &Rating{}
	err := r.db.QueryRow(ctx, `
		SELECT id, ride_id, rater_id, rated_id, direction, score, comment, tags, created_at
		FROM ratings WHERE ride_id = $1 AND rater_id = $2
	`, rideID, raterID).Scan(
		&rat.ID, &rat.RideID, &rat.RaterID, &rat.RatedID,
		&rat.Direction, &rat.Score, &rat.Comment, &rat.Tags, &rat.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return rat, nil
}

func (r *Repository) ListByRated(ctx context.Context, ratedID string, limit, offset int) ([]*Rating, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, ride_id, rater_id, rated_id, direction, score, comment, tags, created_at
		FROM ratings WHERE rated_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, ratedID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ratings []*Rating
	for rows.Next() {
		rat := &Rating{}
		if err := rows.Scan(&rat.ID, &rat.RideID, &rat.RaterID, &rat.RatedID,
			&rat.Direction, &rat.Score, &rat.Comment, &rat.Tags, &rat.CreatedAt); err != nil {
			return nil, err
		}
		ratings = append(ratings, rat)
	}
	return ratings, nil
}

// AvgScore returns the running average for a user (used to update profile rating).
func (r *Repository) AvgScore(ctx context.Context, ratedID string) (float64, int, error) {
	var avg float64
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(AVG(score), 5.0), COUNT(*) FROM ratings WHERE rated_id = $1
	`, ratedID).Scan(&avg, &count)
	return avg, count, err
}

// UpdateDriverProfileRating sets the cached rating on driver_profiles.
func (r *Repository) UpdateDriverProfileRating(ctx context.Context, driverUserID string, avg float64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE driver_profiles SET rating = $1, updated_at = NOW() WHERE user_id = $2
	`, avg, driverUserID)
	return err
}

// UpdateCustomerProfileRating sets the cached rating on customer_profiles.
func (r *Repository) UpdateCustomerProfileRating(ctx context.Context, customerUserID string, avg float64) error {
	_, err := r.db.Exec(ctx, `
		UPDATE customer_profiles SET rating = $1, updated_at = NOW() WHERE user_id = $2
	`, avg, customerUserID)
	return err
}

// FindRideParticipants returns (customer_id, driver_user_id) for a completed ride.
func (r *Repository) FindRideParticipants(ctx context.Context, rideID string) (customerID string, driverUserID string, status string, err error) {
	var driverUID *string
	err = r.db.QueryRow(ctx, `
		SELECT r.customer_id, dp.user_id, r.status
		FROM rides r
		LEFT JOIN driver_profiles dp ON dp.id = r.driver_id
		WHERE r.id = $1
	`, rideID).Scan(&customerID, &driverUID, &status)
	if driverUID != nil {
		driverUserID = *driverUID
	}
	return
}
