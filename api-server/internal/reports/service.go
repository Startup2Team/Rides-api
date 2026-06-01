package reports

import (
	"context"
	"fmt"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) List(ctx context.Context, status, format string, limit, offset int) ([]*Report, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.List(ctx, status, format, limit, offset)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Report, error) {
	rep, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return rep, nil
}

func (s *Service) Generate(ctx context.Context, template, format, dateRange string, createdBy *string) (*Report, error) {
	rep, err := s.repo.Create(ctx, template, format, dateRange, createdBy)
	if err != nil {
		return nil, err
	}

	// Simulate async report generation (in production, enqueue a background job).
	go func() {
		bgCtx := context.Background()
		fakePath := fmt.Sprintf("/reports/%s_%s.%s", template, rep.ID[:8], format)
		fakeSize := "1.2 MB"
		if err := s.repo.MarkReady(bgCtx, rep.ID, fakePath, fakeSize); err != nil {
			_ = s.repo.MarkFailed(bgCtx, rep.ID)
		}
	}()

	return rep, nil
}

func (s *Service) ListScheduled(ctx context.Context) ([]*ScheduledReport, error) {
	return s.repo.ListScheduled(ctx)
}

func (s *Service) CreateScheduled(ctx context.Context, template, format, frequency string, recipients []string) (*ScheduledReport, error) {
	return s.repo.CreateScheduled(ctx, template, format, frequency, recipients)
}

func (s *Service) ToggleScheduled(ctx context.Context, id string) error {
	return s.repo.ToggleScheduled(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperrors.ErrNotFound
	}
	return s.repo.Delete(ctx, id)
}

func (s *Service) GetFilePath(ctx context.Context, id string) (string, error) {
	fp, err := s.repo.GetFilePath(ctx, id)
	if err != nil {
		return "", apperrors.ErrNotFound
	}
	return fp, nil
}
