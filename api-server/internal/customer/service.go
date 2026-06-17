package customer

import "context"

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.FindByID(ctx, userID)
}

func (s *Service) UpdateProfile(ctx context.Context, userID string, fullName, email, fcmToken, profileImageURL *string) error {
	return s.repo.UpdateProfile(ctx, userID, fullName, email, fcmToken, profileImageURL)
}
