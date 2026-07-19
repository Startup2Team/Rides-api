package monetization

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

// ── Partners ──────────────────────────────────────────────────────────────

func (s *Service) ListPartners(ctx context.Context) ([]*Partner, error) {
	return s.repo.ListPartners(ctx)
}

func (s *Service) GetPartnerByID(ctx context.Context, id string) (*Partner, error) {
	p, err := s.repo.GetPartnerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperrors.ErrNotFound
	}
	return p, nil
}

func (s *Service) CreatePartner(ctx context.Context, input CreatePartnerInput) (*Partner, error) {
	if input.Name == "" {
		return nil, apperrors.New(400, "VALIDATION", "partner name is required")
	}
	return s.repo.CreatePartner(ctx, input)
}

func (s *Service) UpdatePartner(ctx context.Context, id string, input UpdatePartnerInput) (*Partner, error) {
	p, err := s.repo.UpdatePartner(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperrors.ErrNotFound
	}
	return p, nil
}

func (s *Service) DeletePartner(ctx context.Context, id string) error {
	p, err := s.repo.GetPartnerByID(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return apperrors.ErrNotFound
	}
	return s.repo.DeletePartner(ctx, id)
}

// ── Adverts ───────────────────────────────────────────────────────────────

func (s *Service) ListAdverts(ctx context.Context) ([]*Advert, error) {
	return s.repo.ListAdverts(ctx)
}

func (s *Service) GetAdvertByID(ctx context.Context, id string) (*Advert, error) {
	a, err := s.repo.GetAdvertByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apperrors.ErrNotFound
	}
	return a, nil
}

func (s *Service) CreateAdvert(ctx context.Context, input CreateAdvertInput) (*Advert, error) {
	if input.PartnerID == "" {
		return nil, apperrors.New(400, "VALIDATION", "partnerId is required")
	}
	if input.Headline == "" {
		return nil, apperrors.New(400, "VALIDATION", "headline is required")
	}
	// Verify partner exists
	p, err := s.repo.GetPartnerByID(ctx, input.PartnerID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperrors.New(404, "PARTNER_NOT_FOUND", "partner not found")
	}
	return s.repo.CreateAdvert(ctx, input)
}

func (s *Service) UpdateAdvert(ctx context.Context, id string, input UpdateAdvertInput) (*Advert, error) {
	if input.PartnerID != nil {
		p, err := s.repo.GetPartnerByID(ctx, *input.PartnerID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, apperrors.New(404, "PARTNER_NOT_FOUND", "partner not found")
		}
	}
	a, err := s.repo.UpdateAdvert(ctx, id, input)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apperrors.ErrNotFound
	}
	return a, nil
}

func (s *Service) DeleteAdvert(ctx context.Context, id string) error {
	a, err := s.repo.GetAdvertByID(ctx, id)
	if err != nil {
		return err
	}
	if a == nil {
		return apperrors.ErrNotFound
	}
	return s.repo.DeleteAdvert(ctx, id)
}

// ── Mobile API ────────────────────────────────────────────────────────────

func (s *Service) ListActiveAdverts(ctx context.Context) ([]*Advert, error) {
	return s.repo.ListActiveAdverts(ctx)
}
