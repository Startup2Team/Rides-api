package customer

import (
	"context"

	"github.com/rs/zerolog"
)

type Service struct {
	repo *Repository
	log  zerolog.Logger
}

func NewService(repo *Repository, log zerolog.Logger) *Service {
	return &Service{repo: repo, log: log}
}

func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.FindByID(ctx, userID)
}

func (s *Service) UpdateProfile(ctx context.Context, userID string, fullName, email, fcmToken, profileImageURL *string) error {
	return s.repo.UpdateProfile(ctx, userID, fullName, email, fcmToken, profileImageURL)
}

// GetLevel computes the customer's gamification level from their lifetime
// completed rides. Read-only and cheap (one aggregate query).
func (s *Service) GetLevel(ctx context.Context, userID string) (*CustomerLevel, error) {
	completed, spend, err := s.repo.RideStats(ctx, userID)
	if err != nil {
		s.log.Error().Err(err).Str("user_id", userID).Msg("customer level: ride stats query failed")
		return nil, err
	}
	lvl := computeLevel(completed, spend)
	s.log.Debug().
		Str("user_id", userID).
		Str("level", lvl.Level).
		Int("completed_rides", lvl.CompletedRides).
		Msg("customer level computed")
	return &lvl, nil
}
