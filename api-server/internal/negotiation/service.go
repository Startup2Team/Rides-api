package negotiation

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/fare"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/telephony"
	"github.com/workspace/ride-platform/internal/tracking"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// maxOffersPerSide matches the frontend: each party gets at most 3 offers.
const maxOffersPerSide = 3

// Service handles fare negotiation business logic.
type Service struct {
	repo      *Repository
	rideRepo  *ride.Repository
	redis     *goredis.Client
	hub       *tracking.Hub
	telephony *telephony.Service
	analytics *analytics.Service
	cfg       *config.Config
	log       zerolog.Logger
	fareRepo  FareConfigRepository
}

type FareConfigRepository interface {
	GetConfigByVehicleType(ctx context.Context, vehicleTypeCode string) (*fare.Config, error)
}

func NewService(repo *Repository, rideRepo *ride.Repository, rdb *goredis.Client, hub *tracking.Hub, tel *telephony.Service, ana *analytics.Service, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{
		repo: repo, rideRepo: rideRepo, redis: rdb, hub: hub,
		telephony: tel, analytics: ana, cfg: cfg, log: log,
	}
}

func (s *Service) SetFareRepository(repo FareConfigRepository) {
	s.fareRepo = repo
}

// Propose submits a fare counter-offer from a customer or driver.
func (s *Service) Propose(ctx context.Context, rideID, actorRole, actorUserID string, amount float64) error {
	r, err := s.rideRepo.FindByID(ctx, rideID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}

	// Enforce per-side limit: count how many offers this role has already made.
	sideCount, err := s.repo.CountRoundsByRole(ctx, rideID, actorRole)
	if err != nil {
		return err
	}
	if sideCount >= maxOffersPerSide {
		return apperrors.ErrNegotiationRoundLimit
	}

	totalCount, err := s.repo.CountRounds(ctx, rideID)
	if err != nil {
		return err
	}

	round, err := s.repo.CreateRound(ctx, rideID, totalCount+1, actorRole, amount)
	if err != nil {
		return fmt.Errorf("create negotiation round: %w", err)
	}

	_ = s.rideRepo.AppendEvent(ctx, rideID, "ride.negotiation_round", actorRole, actorUserID, map[string]interface{}{
		"round_number": round.RoundNumber,
		"proposed_by":  actorRole,
		"amount":       amount,
	})

	s.analytics.Publish(ctx, "ride.negotiation_round", actorRole, actorUserID, &rideID, map[string]interface{}{
		"ride_id":      rideID,
		"round_number": round.RoundNumber,
		"proposed_by":  actorRole,
		"amount":       amount,
	})

	// Notify the other party via WebSocket.
	msg := tracking.Message{
		Type:   "negotiation_message",
		RideID: rideID,
		Payload: map[string]interface{}{
			"round_id":    round.ID,
			"amount":      amount,
			"proposed_by": actorRole,
		},
	}
	if actorRole == "CUSTOMER" {
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, msg)
		}
	} else {
		s.hub.SendToCustomer(rideID, msg)
	}

	return nil
}

// Accept confirms a proposed fare, locking it and transitioning to CONFIRMED.
func (s *Service) Accept(ctx context.Context, rideID, actorRole, actorUserID string) error {
	r, err := s.rideRepo.FindByID(ctx, rideID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}

	latest, err := s.repo.GetLatestRound(ctx, rideID)
	if err != nil {
		return err
	}

	// The accepting party must not be the one who proposed this round.
	if latest.ProposedBy == actorRole {
		return apperrors.New(409, "CANNOT_ACCEPT_OWN_PROPOSAL", "cannot accept your own proposal")
	}
	if s.fareRepo != nil {
		cfg, err := s.fareRepo.GetConfigByVehicleType(ctx, r.TransportType)
		if err == nil && latest.ProposedAmount < float64(cfg.MinFareRWF) {
			return apperrors.New(400, "BELOW_MIN_FARE", fmt.Sprintf("fare cannot be below minimum of %d RWF", cfg.MinFareRWF))
		}
	}

	if err := s.repo.SetResponse(ctx, latest.ID, "ACCEPTED"); err != nil {
		return err
	}

	if err := s.rideRepo.LockFare(ctx, rideID, latest.ProposedAmount); err != nil {
		return err
	}

	if err := s.rideRepo.Transition(ctx, rideID, ride.StatusNegotiating, ride.StatusConfirmed); err != nil {
		return err
	}

	_ = s.rideRepo.AppendEvent(ctx, rideID, "ride.fare_agreed", actorRole, actorUserID, map[string]interface{}{
		"agreed_fare": latest.ProposedAmount,
	})

	s.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_confirmed", RideID: rideID,
		Payload: map[string]interface{}{"agreed_fare": latest.ProposedAmount},
	})
	if r.DriverID != nil {
		s.hub.SendToDriver(*r.DriverID, tracking.Message{
			Type: "ride_confirmed", RideID: rideID,
			Payload: map[string]interface{}{"agreed_fare": latest.ProposedAmount},
		})
	}

	s.analytics.Publish(ctx, "ride.fare_agreed", actorRole, actorUserID, &rideID, map[string]interface{}{
		"ride_id":     rideID,
		"agreed_fare": latest.ProposedAmount,
	})

	return nil
}

// LockManualFare confirms a verbally agreed fare without consuming offer rounds.
func (s *Service) LockManualFare(ctx context.Context, rideID, driverUserID string, amount float64) error {
	r, err := s.rideRepo.FindByID(ctx, rideID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}
	if s.fareRepo != nil {
		cfg, err := s.fareRepo.GetConfigByVehicleType(ctx, r.TransportType)
		if err == nil && amount < float64(cfg.MinFareRWF) {
			return apperrors.New(400, "BELOW_MIN_FARE", fmt.Sprintf("fare cannot be below minimum of %d RWF", cfg.MinFareRWF))
		}
	}

	if err := s.rideRepo.LockFare(ctx, rideID, amount); err != nil {
		return err
	}
	if err := s.rideRepo.Transition(ctx, rideID, ride.StatusNegotiating, ride.StatusConfirmed); err != nil {
		return err
	}

	_ = s.rideRepo.AppendEvent(ctx, rideID, "ride.fare_agreed_manual", "DRIVER", driverUserID, map[string]interface{}{
		"agreed_fare": amount,
	})

	msg := tracking.Message{
		Type:    "ride_confirmed",
		RideID:  rideID,
		Payload: map[string]interface{}{"agreed_fare": amount, "manual": true},
	}
	s.hub.SendToCustomer(rideID, msg)
	if r.DriverID != nil {
		s.hub.SendToDriver(*r.DriverID, msg)
	}

	s.analytics.Publish(ctx, "ride.fare_agreed_manual", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id":     rideID,
		"agreed_fare": amount,
	})

	return nil
}

// Decline rejects a proposed fare. Negotiation continues until limits are hit.
func (s *Service) Decline(ctx context.Context, rideID, actorRole, actorUserID string) error {
	r, err := s.rideRepo.FindByID(ctx, rideID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}

	latest, err := s.repo.GetLatestRound(ctx, rideID)
	if err != nil {
		return err
	}

	return s.repo.SetResponse(ctx, latest.ID, "DECLINED")
}

// InitiateCall logs the call timestamp and returns the Africa's Talking masking number.
func (s *Service) InitiateCall(ctx context.Context, rideID, driverUserID string) (string, error) {
	r, err := s.rideRepo.FindByID(ctx, rideID)
	if err != nil {
		return "", err
	}
	if r.Status != ride.StatusNegotiating {
		return "", apperrors.ErrInvalidTransition
	}

	if err := s.repo.MarkCallInitiated(ctx, rideID); err != nil {
		return "", err
	}

	s.redis.HSet(ctx, rkeys.K.RideNegotiation(rideID), "call_initiated_at", "true")

	maskedNumber, err := s.telephony.GetMaskedNumber(ctx, rideID)
	if err != nil {
		return "", err
	}

	s.analytics.Publish(ctx, "ride.call_initiated", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id":   rideID,
		"driver_id": driverUserID,
	})

	return maskedNumber, nil
}
