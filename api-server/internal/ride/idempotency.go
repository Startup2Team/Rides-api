package ride

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (repo *Repository) FindRideByIdempotency(ctx context.Context, actorUserID, key string) (*Ride, error) {
	if key == "" {
		return nil, nil
	}
	var rideID string
	err := repo.db.QueryRow(ctx, `
		SELECT ride_id FROM ride_command_idempotency
		WHERE actor_user_id = $1 AND idempotency_key = $2
	`, actorUserID, key).Scan(&rideID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return repo.FindByID(ctx, rideID)
}

func (repo *Repository) SaveRideIdempotency(ctx context.Context, actorUserID, key, rideID string) error {
	if key == "" {
		return nil
	}
	_, err := repo.db.Exec(ctx, `
		INSERT INTO ride_command_idempotency (actor_user_id, idempotency_key, ride_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (actor_user_id, idempotency_key) DO NOTHING
	`, actorUserID, key, rideID)
	return err
}
