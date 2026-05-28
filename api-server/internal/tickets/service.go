package tickets

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

func (s *Service) List(ctx context.Context, f ListFilter) ([]*Ticket, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	return s.repo.List(ctx, f)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Ticket, error) {
	t, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return t, nil
}

func (s *Service) Reply(ctx context.Context, id, author, body string) error {
	t, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return apperrors.ErrNotFound
	}
	if err := s.repo.AddMessage(ctx, id, "agent", author, body); err != nil {
		return err
	}
	if t.Status == "OPEN" {
		return s.repo.UpdateStatus(ctx, id, "PENDING")
	}
	return nil
}

func (s *Service) Assign(ctx context.Context, id, adminID string) error {
	return s.repo.Assign(ctx, id, adminID)
}

func (s *Service) Resolve(ctx context.Context, id string) error {
	return s.repo.UpdateStatus(ctx, id, "RESOLVED")
}

func (s *Service) Create(ctx context.Context, subject, ticketType, priority, fromRole string, fromUserID, rideID *string) (*Ticket, error) {
	return s.repo.Create(ctx, subject, ticketType, priority, fromRole, fromUserID, rideID)
}
