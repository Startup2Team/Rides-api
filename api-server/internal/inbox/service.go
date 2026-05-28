package inbox

import (
	"context"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]*Message, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	return s.repo.List(ctx, f)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Message, error) {
	m, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return m, nil
}

func (s *Service) Reply(ctx context.Context, id, replyBody string) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperrors.ErrNotFound
	}
	return s.repo.Reply(ctx, id, replyBody)
}

func (s *Service) Archive(ctx context.Context, id string) error {
	return s.repo.UpdateStatus(ctx, id, "ARCHIVED")
}

func (s *Service) MarkSpam(ctx context.Context, id string) error {
	return s.repo.UpdateStatus(ctx, id, "SPAM")
}
