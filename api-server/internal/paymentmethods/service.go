package paymentmethods

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

var validProviders = map[string]bool{"mtn": true, "airtel": true, "cash": true}

func (s *Service) List(ctx context.Context, userID string) ([]*Method, error) {
	return s.repo.List(ctx, userID)
}

func (s *Service) Default(ctx context.Context, userID string) (*Method, error) {
	return s.repo.Default(ctx, userID)
}

// BillingProfile derives the preferences summary from the method list.
func (s *Service) BillingProfile(ctx context.Context, userID string) (*BillingProfile, error) {
	methods, err := s.repo.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	bp := &BillingProfile{
		MobileMoneyMethodIDs: []string{},
		CardMethodIDs:        []string{},
		CashEnabled:          true, // cash is always an available option
	}
	for _, m := range methods {
		if m.IsDefault {
			id := m.ID
			bp.DefaultPaymentMethodID = &id
		}
		if m.Provider == "mtn" || m.Provider == "airtel" {
			bp.MobileMoneyMethodIDs = append(bp.MobileMoneyMethodIDs, m.ID)
		}
	}
	return bp, nil
}

func (s *Service) Add(ctx context.Context, userID string, in AddInput) ([]*Method, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if !validProviders[in.Provider] {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_PROVIDER", "provider must be one of mtn, airtel, cash")
	}
	if strings.TrimSpace(in.Label) == "" {
		return nil, apperrors.New(http.StatusUnprocessableEntity, "INVALID_LABEL", "label is required")
	}
	if in.Provider == "cash" {
		in.PhoneNumber = nil
	}

	// Idempotent create: a repeated key returns the existing state, no duplicate.
	if in.IdempotencyKey != "" {
		if existing, err := s.repo.FindByIdempotencyKey(ctx, userID, in.IdempotencyKey); err == nil && existing != nil {
			return s.repo.List(ctx, userID)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	if _, err := s.repo.Insert(ctx, userID, in); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, userID)
}

func (s *Service) Update(ctx context.Context, userID, id string, in UpdateInput) ([]*Method, error) {
	if _, err := s.repo.FindByID(ctx, userID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	if err := s.repo.Update(ctx, userID, id, in); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, userID)
}

func (s *Service) SetDefault(ctx context.Context, userID, id string) ([]*Method, error) {
	if _, err := s.repo.FindByID(ctx, userID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperrors.ErrNotFound
		}
		return nil, err
	}
	if err := s.repo.SetDefault(ctx, userID, id); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, userID)
}

func (s *Service) Delete(ctx context.Context, userID, id string) ([]*Method, error) {
	n, err := s.repo.Delete(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, apperrors.ErrNotFound
	}
	return s.repo.List(ctx, userID)
}
