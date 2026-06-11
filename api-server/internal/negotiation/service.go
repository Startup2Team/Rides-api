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

// TimeoutManager resets/cancels the negotiation inactivity clock and charges
// the ride credit at fare agreement. Implemented by ride.Service; injected via
// SetTimeoutManager to avoid an import cycle (negotiation → ride is fine;
// ride → negotiation would cycle).
type TimeoutManager interface {
	ResetNegotiationTimeout(rideID string)
	CancelNegotiationTimeout(rideID string)
	// ChargeForAgreedFare deducts the driver's ride credit the moment a fare is
	// agreed (NEGOTIATING → CONFIRMED). Idempotent per ride.
	ChargeForAgreedFare(ctx context.Context, rideID string)
}

// Service handles fare negotiation business logic.
type Service struct {
	repo       *Repository
	rideRepo   *ride.Repository
	redis      *goredis.Client
	hub        *tracking.Hub
	telephony  *telephony.Service
	analytics  *analytics.Service
	cfg        *config.Config
	log        zerolog.Logger
	fareRepo   FareConfigRepository
	timeoutMgr TimeoutManager
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

// SetTimeoutManager wires the ride.Service timer so negotiation activity resets it.
func (s *Service) SetTimeoutManager(mgr TimeoutManager) {
	s.timeoutMgr = mgr
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
		// Both sides have hit their offer limits — prompt them to call each other
		// and use LockManualFare once they verbally agree, instead of letting the
		// negotiation timeout silently kill the ride.
		s.notifyCallPrompt(ctx, rideID, r)
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

	// Each counter-offer is activity — reset the inactivity clock so the 5-minute
	// window only triggers after a true silence, not while parties are bargaining.
	if s.timeoutMgr != nil {
		s.timeoutMgr.ResetNegotiationTimeout(rideID)
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
		if err == nil {
			if latest.ProposedAmount < float64(cfg.MinFareRWF) {
				return apperrors.New(400, "BELOW_MIN_FARE", fmt.Sprintf("fare cannot be below minimum of %d RWF", cfg.MinFareRWF))
			}
			maxFare := float64(cfg.MinFareRWF) * maxFareMultiplier
			if latest.ProposedAmount > maxFare {
				return apperrors.New(400, "ABOVE_MAX_FARE", fmt.Sprintf("fare cannot exceed %d RWF", int(maxFare)))
			}
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

	// Fare is agreed — disarm the inactivity timer cleanly.
	if s.timeoutMgr != nil {
		// Fare agreed → charge the driver's credit now (closes the complete-dodge
		// loophole), and disarm the inactivity timer.
		s.timeoutMgr.ChargeForAgreedFare(ctx, rideID)
		s.timeoutMgr.CancelNegotiationTimeout(rideID)
	}

	return nil
}

// maxFareMultiplier caps a manually-locked fare at 10× the minimum fare to
// prevent drivers from accidentally (or maliciously) locking absurd amounts.
const maxFareMultiplier = 10

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
		if err == nil {
			if amount < float64(cfg.MinFareRWF) {
				return apperrors.New(400, "BELOW_MIN_FARE", fmt.Sprintf("fare cannot be below minimum of %d RWF", cfg.MinFareRWF))
			}
			maxFare := float64(cfg.MinFareRWF) * maxFareMultiplier
			if amount > maxFare {
				return apperrors.New(400, "ABOVE_MAX_FARE", fmt.Sprintf("fare cannot exceed %d RWF", int(maxFare)))
			}
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

	if s.timeoutMgr != nil {
		// Fare agreed → charge the driver's credit now (closes the complete-dodge
		// loophole), and disarm the inactivity timer.
		s.timeoutMgr.ChargeForAgreedFare(ctx, rideID)
		s.timeoutMgr.CancelNegotiationTimeout(rideID)
	}

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

	if err := s.repo.SetResponse(ctx, latest.ID, "DECLINED"); err != nil {
		return err
	}

	// Notify the other party so they see the rejection in real time
	// instead of waiting for a poll or timeout.
	msg := tracking.Message{
		Type:   "negotiation_declined",
		RideID: rideID,
		Payload: map[string]interface{}{
			"declined_by": actorRole,
		},
	}
	if actorRole == "CUSTOMER" {
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, msg)
		}
	} else {
		s.hub.SendToCustomer(rideID, msg)
	}

	s.analytics.Publish(ctx, "ride.negotiation_declined", actorRole, actorUserID, &rideID, map[string]interface{}{
		"ride_id":     rideID,
		"declined_by": actorRole,
	})

	return nil
}

// notifyCallPrompt is sent to both parties when offer rounds are exhausted.
// It tells the mobile apps to show the "Call to negotiate" UI so both sides
// can agree verbally and one of them types the final fare into LockManualFare.
func (s *Service) notifyCallPrompt(ctx context.Context, rideID string, r *ride.Ride) {
	msg := tracking.Message{
		Type:   "negotiation_call_prompt",
		RideID: rideID,
		Payload: map[string]interface{}{
			"message": "Offer rounds exhausted. Call the other party to agree on a fare, then lock it manually.",
		},
	}
	s.hub.SendToCustomer(rideID, msg)
	if r.DriverID != nil {
		s.hub.SendToDriver(*r.DriverID, msg)
	}
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
