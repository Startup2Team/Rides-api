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

// Fare-band tuning. The acceptable negotiation range is anchored on the trip's
// fare estimate so heavy vehicles (Hilux/Fuso) on long hauls aren't capped
// below their own metered fare. A flat min_fare×N ceiling breaks for these:
// e.g. a ~40 km Fuso meters ~25,400 RWF but min_fare(2000)×10 caps at 20,000,
// so the driver literally cannot accept the fair price. The estimate-scaled
// band lifts with distance automatically, while the flat band remains the
// floor/ceiling when no estimate is present.
const (
	// maxFareMultiplier caps a locked fare at 10× the minimum fare when there is
	// no estimate to anchor on — prevents locking absurd amounts on a bare ride.
	maxFareMultiplier = 10
	// fareBandFloorPct: parties may bargain down to 60% of the estimate, but
	// never below the vehicle's min_fare (whichever is higher wins).
	fareBandFloorPct = 0.60
	// fareBandCeilPct: and up to 180% of the estimate, or the flat min_fare×10
	// ceiling, whichever is higher.
	fareBandCeilPct = 1.80
)

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
	// NotifyFareConfirmed pushes an in-app + FCM "ride confirmed" notification to
	// the customer and the assigned driver on fare agreement.
	NotifyFareConfirmed(ctx context.Context, customerID string, driverProfileID *string, amount float64)
	// NotifyNegotiationOffer pushes a counter-offer notification to the party that
	// did not propose it.
	NotifyNegotiationOffer(ctx context.Context, rideID, customerID string, driverProfileID *string, proposerRole string, amount float64)
}

// Service handles fare negotiation business logic.
type Service struct {
	repo       *Repository
	rideRepo   *ride.Repository
	redis      goredis.UniversalClient
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

func NewService(repo *Repository, rideRepo *ride.Repository, rdb goredis.UniversalClient, hub *tracking.Hub, tel *telephony.Service, ana *analytics.Service, cfg *config.Config, log zerolog.Logger) *Service {
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

// findRideForActor loads the ride ONLY if the caller is the participant they
// claim to be — the customer who owns it, or the driver assigned to it. This is
// the authorization boundary for every negotiation action: it stops an
// authenticated user from driving (or accepting) a fare on a ride that isn't
// theirs just by knowing the ride_id (IDOR).
func (s *Service) findRideForActor(ctx context.Context, rideID, actorRole, actorUserID string) (*ride.Ride, error) {
	if actorRole == "CUSTOMER" {
		return s.rideRepo.FindByIDAndCustomer(ctx, rideID, actorUserID)
	}
	return s.rideRepo.FindByIDAndDriver(ctx, rideID, actorUserID)
}

// fareBounds returns the acceptable [min,max] fare band for a ride. The band is
// anchored on the fare estimate when one exists (so long-distance heavy-vehicle
// trips aren't capped below their metered fare) and falls back to the flat
// min_fare … min_fare×10 band when no estimate is available. Both edges only
// ever widen the flat band, never shrink it, so short trips keep the min_fare
// floor.
//
// enforced is false only when no fare config is wired (unit tests). A config
// lookup FAILURE returns an error so the money-committing callers fail closed
// rather than silently skipping the guard.
func (s *Service) fareBounds(ctx context.Context, r *ride.Ride) (minFare, maxFare float64, enforced bool, err error) {
	if s.fareRepo == nil {
		return 0, 0, false, nil
	}
	cfg, err := s.fareRepo.GetConfigByVehicleType(ctx, r.TransportType)
	if err != nil {
		return 0, 0, false, apperrors.New(503, "FARE_CONFIG_UNAVAILABLE", "cannot determine fare limits right now, please retry")
	}
	minFare = float64(cfg.MinFareRWF)
	maxFare = float64(cfg.MinFareRWF) * maxFareMultiplier
	if r.EstimatedFareRWF != nil && *r.EstimatedFareRWF > 0 {
		est := *r.EstimatedFareRWF
		if f := est * fareBandFloorPct; f > minFare {
			minFare = f
		}
		if c := est * fareBandCeilPct; c > maxFare {
			maxFare = c
		}
	}
	return minFare, maxFare, true, nil
}

// checkFareInBand rejects an amount outside the ride's acceptable fare band. It
// is a no-op when enforcement is disabled (no fare config wired) and propagates
// the fail-closed error from fareBounds when the config cannot be read.
func (s *Service) checkFareInBand(ctx context.Context, r *ride.Ride, amount float64) error {
	minFare, maxFare, enforced, err := s.fareBounds(ctx, r)
	if err != nil {
		return err
	}
	if !enforced {
		return nil
	}
	if amount < minFare {
		return apperrors.New(400, "BELOW_MIN_FARE", fmt.Sprintf("fare cannot be below %d RWF for this trip", int(minFare)))
	}
	if amount > maxFare {
		return apperrors.New(400, "ABOVE_MAX_FARE", fmt.Sprintf("fare cannot exceed %d RWF for this trip", int(maxFare)))
	}
	return nil
}

// Propose submits a fare counter-offer from a customer or driver.
func (s *Service) Propose(ctx context.Context, rideID, actorRole, actorUserID string, amount float64) error {
	r, err := s.findRideForActor(ctx, rideID, actorRole, actorUserID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}

	// Reject out-of-band proposals up front so a party can't fill their offer
	// rounds with amounts that could never be accepted.
	if err := s.checkFareInBand(ctx, r, amount); err != nil {
		return err
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
	// Also push an in-app + FCM notification to the recipient so a backgrounded
	// app surfaces the new offer, not just the live WebSocket.
	if s.timeoutMgr != nil {
		s.timeoutMgr.ResetNegotiationTimeout(rideID)
		s.timeoutMgr.NotifyNegotiationOffer(ctx, rideID, r.CustomerID, r.DriverID, actorRole, amount)
	}

	return nil
}

// Accept confirms a proposed fare, locking it and transitioning to CONFIRMED.
func (s *Service) Accept(ctx context.Context, rideID, actorRole, actorUserID string) error {
	r, err := s.findRideForActor(ctx, rideID, actorRole, actorUserID)
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
	if err := s.checkFareInBand(ctx, r, latest.ProposedAmount); err != nil {
		return err
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
		// loophole), disarm the inactivity timer, and push "ride confirmed" to both
		// parties so a backgrounded app wakes.
		s.timeoutMgr.ChargeForAgreedFare(ctx, rideID)
		s.timeoutMgr.CancelNegotiationTimeout(rideID)
		s.timeoutMgr.NotifyFareConfirmed(ctx, r.CustomerID, r.DriverID, latest.ProposedAmount)
	}

	return nil
}

// LockManualFare confirms a verbally agreed fare without consuming offer rounds.
func (s *Service) LockManualFare(ctx context.Context, rideID, driverUserID string, amount float64) error {
	// Only the assigned driver may lock a manual fare on this ride.
	r, err := s.rideRepo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}
	if err := s.checkFareInBand(ctx, r, amount); err != nil {
		return err
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
		// loophole), disarm the inactivity timer, and push "ride confirmed".
		s.timeoutMgr.ChargeForAgreedFare(ctx, rideID)
		s.timeoutMgr.CancelNegotiationTimeout(rideID)
		s.timeoutMgr.NotifyFareConfirmed(ctx, r.CustomerID, r.DriverID, amount)
	}

	return nil
}

// Decline rejects a proposed fare. Negotiation continues until limits are hit.
func (s *Service) Decline(ctx context.Context, rideID, actorRole, actorUserID string) error {
	r, err := s.findRideForActor(ctx, rideID, actorRole, actorUserID)
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

// SendTextMessage persists a chat message and pushes it to the other party via WebSocket.
func (s *Service) SendTextMessage(ctx context.Context, rideID, actorRole, actorUserID, body string) error {
	r, err := s.findRideForActor(ctx, rideID, actorRole, actorUserID)
	if err != nil {
		return err
	}
	if r.Status != ride.StatusNegotiating {
		return apperrors.ErrInvalidTransition
	}

	msg, err := s.repo.CreateTextMessage(ctx, rideID, actorRole, body)
	if err != nil {
		return fmt.Errorf("create negotiation text message: %w", err)
	}

	wsMsg := tracking.Message{
		Type:   "negotiation_text",
		RideID: rideID,
		Payload: map[string]interface{}{
			"id":      msg.ID,
			"sender":  actorRole,
			"body":    body,
			"sent_at": msg.CreatedAt,
		},
	}
	if actorRole == "CUSTOMER" {
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, wsMsg)
		}
	} else {
		s.hub.SendToCustomer(rideID, wsMsg)
	}

	if s.timeoutMgr != nil {
		s.timeoutMgr.ResetNegotiationTimeout(rideID)
	}

	return nil
}

// HistoryEntry is a unified timeline item (offer or text) for a ride's negotiation.
type HistoryEntry struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"` // "offer" | "text"
	Sender    string      `json:"sender"`
	Amount    *float64    `json:"amount,omitempty"`
	Response  *string     `json:"response,omitempty"`
	Text      *string     `json:"text,omitempty"`
	IsFinal   bool        `json:"is_final,omitempty"`
	Timestamp interface{} `json:"timestamp"`
}

// GetHistory returns the merged timeline of offers and text messages for a ride.
func (s *Service) GetHistory(ctx context.Context, rideID, actorRole, actorUserID string) ([]HistoryEntry, error) {
	_, err := s.findRideForActor(ctx, rideID, actorRole, actorUserID)
	if err != nil {
		return nil, err
	}

	rounds, err := s.repo.ListRounds(ctx, rideID)
	if err != nil {
		return nil, err
	}
	texts, err := s.repo.ListTextMessages(ctx, rideID)
	if err != nil {
		return nil, err
	}

	var entries []HistoryEntry
	ri, ti := 0, 0
	for ri < len(rounds) || ti < len(texts) {
		addRound := false
		if ri < len(rounds) && ti < len(texts) {
			addRound = rounds[ri].CreatedAt.Before(texts[ti].CreatedAt) || rounds[ri].CreatedAt.Equal(texts[ti].CreatedAt)
		} else {
			addRound = ri < len(rounds)
		}

		if addRound {
			r := rounds[ri]
			sender := "driver"
			if r.ProposedBy == "CUSTOMER" {
				sender = "customer"
			}
			amt := r.ProposedAmount
			e := HistoryEntry{
				ID:        r.ID,
				Type:      "offer",
				Sender:    sender,
				Amount:    &amt,
				Response:  r.Response,
				Text:      r.Message,
				Timestamp: r.CreatedAt,
			}
			if r.Response != nil && *r.Response == "ACCEPTED" {
				e.IsFinal = true
			}
			entries = append(entries, e)
			ri++
		} else {
			t := texts[ti]
			sender := "driver"
			if t.Sender == "CUSTOMER" {
				sender = "customer"
			} else if t.Sender == "SYSTEM" {
				sender = "system"
			}
			entries = append(entries, HistoryEntry{
				ID:        t.ID,
				Type:      "text",
				Sender:    sender,
				Text:      &t.Body,
				Timestamp: t.CreatedAt,
			})
			ti++
		}
	}

	return entries, nil
}

// InitiateCall logs the call timestamp and returns the Africa's Talking masking number.
func (s *Service) InitiateCall(ctx context.Context, rideID, driverUserID string) (string, error) {
	// Only the assigned driver may pull the masked number for this ride.
	r, err := s.rideRepo.FindByIDAndDriver(ctx, rideID, driverUserID)
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
