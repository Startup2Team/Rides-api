package inbox

import (
	"context"
	"strings"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// Submit saves a contact-form submission from the public website.
func (s *Service) Submit(ctx context.Context, fromName, fromEmail, phone, category, subject, body string) error {
	// Normalise category to uppercase for consistent storage.
	cat := strings.ToUpper(strings.ReplaceAll(category, " ", "_"))
	if cat == "" {
		cat = "GENERAL"
	}
	// Prepend phone to the body when provided so it's visible in the admin inbox.
	fullBody := body
	if phone != "" {
		fullBody = "Phone: " + phone + "\n\n" + body
	}
	return s.repo.Create(ctx, fromName, fromEmail, cat, subject, fullBody)
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

func (s *Service) UpdateStatus(ctx context.Context, id, status string) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperrors.ErrNotFound
	}
	return s.repo.UpdateStatus(ctx, id, status)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperrors.ErrNotFound
	}
	return s.repo.Delete(ctx, id)
}

func (s *Service) Stats(ctx context.Context) (map[string]interface{}, error) {
	return s.repo.Stats(ctx)
}
