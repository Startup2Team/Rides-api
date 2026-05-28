package incidents

import (
	"context"
	"fmt"
	"net/http"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]*Incident, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	return s.repo.List(ctx, f)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Incident, error) {
	inc, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return inc, nil
}

func (s *Service) Acknowledge(ctx context.Context, id string) error {
	inc, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return apperrors.ErrNotFound
	}
	if inc.Status != "OPEN" {
		return apperrors.New(http.StatusConflict, "INVALID_STATE", "incident is not in OPEN state")
	}
	if err := s.repo.UpdateStatus(ctx, id, "ACKNOWLEDGED"); err != nil {
		return err
	}
	return s.repo.AppendEvent(ctx, id, "Incident acknowledged by ops", "ops")
}

func (s *Service) Escalate(ctx context.Context, id string) error {
	if err := s.repo.UpdateStatus(ctx, id, "ESCALATED"); err != nil {
		return err
	}
	return s.repo.AppendEvent(ctx, id, "Incident escalated", "alert")
}

func (s *Service) Resolve(ctx context.Context, id, notes string) error {
	if notes != "" {
		_ = s.repo.UpdateNotes(ctx, id, notes)
	}
	if err := s.repo.UpdateStatus(ctx, id, "RESOLVED"); err != nil {
		return err
	}
	return s.repo.AppendEvent(ctx, id, "Incident marked resolved", "system")
}

func (s *Service) AddMessage(ctx context.Context, id, message string) error {
	return s.repo.AppendEvent(ctx, id, fmt.Sprintf("Message: %s", message), "ops")
}

func (s *Service) Create(ctx context.Context, incType, severity, description, reporterRole, locationText, district string, rideID, reporterUserID *string) (*Incident, error) {
	inc, err := s.repo.Create(ctx, incType, severity, description, reporterRole, locationText, district, rideID, reporterUserID)
	if err != nil {
		return nil, err
	}
	_ = s.repo.AppendEvent(ctx, inc.ID, "Incident created", "system")
	return inc, nil
}
