package settings

import "context"

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetAll(ctx context.Context) (map[string]interface{}, error) {
	return s.repo.GetAll(ctx)
}

func (s *Service) Update(ctx context.Context, key string, value interface{}) error {
	return s.repo.Set(ctx, key, value)
}

func (s *Service) UpdateRegion(ctx context.Context, regionID string, updates map[string]interface{}) error {
	return s.repo.UpdateRegion(ctx, regionID, updates)
}

func (s *Service) CreateRegion(ctx context.Context, name, status string) (map[string]interface{}, error) {
	return s.repo.CreateRegion(ctx, name, status)
}

func (s *Service) DeleteRegion(ctx context.Context, regionID string) error {
	return s.repo.DeleteRegion(ctx, regionID)
}
