package ride

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
	"github.com/workspace/ride-platform/pkg/timeutil"

	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/fare"
	"github.com/workspace/ride-platform/internal/notification"
	"github.com/workspace/ride-platform/internal/tracking"
)

const (
	negotiationTimeoutDuration = 5 * time.Minute
	pickupExpiryDuration       = 5 * time.Minute
	driverStateAvailable       = "AVAILABLE"
)

// MatchingEngineInterface breaks the import cycle between ride ↔ matching.
type MatchingEngineInterface interface {
	StartSearch(rideID string, pickup geo.Point, transportType string)
}

type RouteFareRecorder interface {
	RecordAgreedFare(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string, agreedFare float64)
}

// PackagesService charges a ride credit when a fare is agreed and refunds it
// on server-verified blameless cancellations.
type PackagesService interface {
	DeductCredit(ctx context.Context, driverUserID string) error
	RefundCredit(ctx context.Context, driverUserID string) error
}

// Service handles ride lifecycle business logic.
type Service struct {
	repo      *Repository
	redis     *goredis.Client
	notify    *notification.Service
	analytics *analytics.Service
	hub       *tracking.Hub
	cfg       *config.Config
	log       zerolog.Logger
	engine    MatchingEngineInterface
	routes    RouteFareRecorder
	packages  PackagesService
	fareRepo  FareConfigRepository

	// negTimers tracks the active negotiation inactivity timer for each ride.
	// keyed by rideID → *time.Timer; cleaned up on cancel, accept, or timeout.
	negTimers sync.Map
}

type FareConfigRepository interface {
	GetActiveConfig(ctx context.Context, vehicleTypeCode string) (*fare.Config, error)
	GetConfigByID(ctx context.Context, id string) (*fare.Config, error)
}

func NewService(repo *Repository, rdb *goredis.Client, notify *notification.Service, ana *analytics.Service, hub *tracking.Hub, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{repo: repo, redis: rdb, notify: notify, analytics: ana, hub: hub, cfg: cfg, log: log}
}

func (s *Service) SetMatchingEngine(engine MatchingEngineInterface) {
	s.engine = engine
}

func (s *Service) SetRouteFareRecorder(routes RouteFareRecorder) {
	s.routes = routes
}

func (s *Service) SetPackagesService(svc PackagesService) {
	s.packages = svc
}

func (s *Service) SetFareRepository(repo FareConfigRepository) {
	s.fareRepo = repo
}

// CreateRide creates a new ride in SEARCHING status and triggers matching.
func (s *Service) CreateRide(ctx context.Context, customerID, transportType, pickupAddr, destAddr string, pickup, dest geo.Point, initialFare, distanceKM *float64) (*Ride, error) {
	// ── Concurrent-creation guard ─────────────────────────────────────────────
	// Use SET NX with a 10-second TTL to prevent two simultaneous requests from
	// the same customer (e.g. double-tap, retry storm) creating duplicate rides.
	// The key is deleted explicitly on success so the TTL is just a safety net.
	createLockKey := rkeys.K.CustomerCreatingRide(customerID)
	locked, err := s.redis.SetNX(ctx, createLockKey, "1", 10*time.Second).Result()
	if err != nil {
		return nil, fmt.Errorf("ride: acquire create lock: %w", err)
	}
	if !locked {
		return nil, apperrors.ErrRideAlreadyActive
	}
	defer s.redis.Del(ctx, createLockKey)

	// Also reject immediately if the customer already has a live ride in Redis.
	// This catches the case where the client somehow bypasses the UI guard.
	if existing, _ := s.redis.Get(ctx, rkeys.K.CustomerActiveRide(customerID)).Result(); existing != "" {
		return nil, apperrors.ErrRideAlreadyActive
	}

	key := rkeys.K.CustomerDailyCancel(customerID)
	count, _ := s.redis.Get(ctx, key).Int()
	if count >= s.cfg.Customer.CancelSuspendThreshold {
		return nil, apperrors.ErrCustomerSuspended
	}

	var estimatedFare *float64
	var pricingConfigID *string
	if s.fareRepo != nil {
		pricingCfg, err := s.fareRepo.GetActiveConfig(ctx, transportType)
		if err != nil {
			return nil, fmt.Errorf("pricing config unavailable: %w", err)
		}
		pricingConfigID = &pricingCfg.ID
		if distanceKM != nil {
			b := fare.Calculate(pricingCfg, *distanceKM, time.Now(), 0)
			estimatedFare = &b.TotalFare
		}
	}

	r, err := s.repo.CreateRide(ctx, customerID, transportType, pickupAddr, destAddr, pickup, dest, initialFare, estimatedFare, pricingConfigID)
	if err != nil {
		return nil, err
	}

	// 15-minute TTL: matching takes at most MaxAttempts × TimeoutSeconds (≈45s).
	// If the server crashes mid-search and the recovery job misses this ride,
	// the key will self-expire rather than being stuck at SEARCHING forever.
	if err := s.redis.Set(ctx, rkeys.K.RideState(r.ID), string(StatusSearching), 15*time.Minute).Err(); err != nil {
		s.log.Error().Err(err).Str("ride_id", r.ID).Msg("ride: failed to write ride state to Redis")
	}
	if err := s.redis.Set(ctx, rkeys.K.CustomerActiveRide(customerID), r.ID, 0).Err(); err != nil {
		s.log.Error().Err(err).Str("ride_id", r.ID).Str("customer_id", customerID).Msg("ride: failed to write customer active ride to Redis")
	}

	_ = s.repo.AppendEvent(ctx, r.ID, "ride.created", "CUSTOMER", customerID, map[string]interface{}{
		"transport_type": transportType,
		"pickup_address": pickupAddr,
		"dest_address":   destAddr,
	})
	s.analytics.Publish(ctx, "ride.created", "CUSTOMER", customerID, &r.ID, map[string]interface{}{
		"ride_id": r.ID, "customer_id": customerID, "transport_type": transportType,
	})

	if s.engine != nil {
		s.engine.StartSearch(r.ID, pickup, transportType)
	}
	return r, nil
}

// GetRide retrieves a ride by ID for a customer.
func (s *Service) GetRide(ctx context.Context, rideID, customerID string) (*Ride, error) {
	r, err := s.repo.FindByIDAndCustomer(ctx, rideID, customerID)
	if err != nil {
		// Self-heal: if the ride can't be found, clean up any stale Redis state
		// for it so the mobile app stops looping on a ghost ride ID.
		// This happens when CompleteRide's Redis write fails silently after the
		// DB write succeeds — the ride:state key gets stuck at IN_PROGRESS forever.
		s.redis.Del(ctx, rkeys.K.RideState(rideID))
		// Also clear the customer's active_ride pointer if it still points here.
		if cached, cerr := s.redis.Get(ctx, rkeys.K.CustomerActiveRide(customerID)).Result(); cerr == nil && cached == rideID {
			s.redis.Del(ctx, rkeys.K.CustomerActiveRide(customerID))
			s.log.Warn().Str("ride_id", rideID).Str("customer_id", customerID).Msg("ride: cleaned up stale Redis active_ride pointer on 404")
		}
		return nil, err
	}
	switch r.Status {
	case StatusSearching, StatusMatched, StatusNegotiating:
		r.DriverID = nil
	}
	return r, nil
}

// GetActiveRide returns the customer's current active ride for reconnect recovery.
func (s *Service) GetActiveRide(ctx context.Context, customerID string) (*Ride, error) {
	rideID, err := s.redis.Get(ctx, rkeys.K.CustomerActiveRide(customerID)).Result()
	if err != nil {
		return s.repo.FindActiveByCustomer(ctx, customerID)
	}
	r, err := s.repo.FindByIDAndCustomer(ctx, rideID, customerID)
	if err != nil {
		// Redis pointer is stale (ride completed/cancelled but key wasn't cleaned up).
		// Delete it so the next call goes straight to the DB fallback.
		s.redis.Del(ctx, rkeys.K.CustomerActiveRide(customerID))
		s.log.Warn().Str("ride_id", rideID).Str("customer_id", customerID).Msg("ride: deleted stale customer active_ride Redis key")
		return s.repo.FindActiveByCustomer(ctx, customerID)
	}
	// Also self-heal: if the ride we found is terminal, the Redis key should be gone.
	if IsTerminal(r.Status) {
		s.redis.Del(ctx, rkeys.K.CustomerActiveRide(customerID))
		s.redis.Del(ctx, rkeys.K.RideState(rideID))
		s.log.Warn().Str("ride_id", rideID).Str("status", string(r.Status)).Msg("ride: cleaned up terminal ride from active_ride Redis key")
		return nil, apperrors.ErrNotFound
	}
	return r, nil
}

// GetRideForDriver retrieves a ride by ID for an assigned driver.
func (s *Service) GetRideForDriver(ctx context.Context, rideID, driverUserID string) (*Ride, error) {
	return s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
}

// GetActiveRideForDriver returns the driver's current active ride.
func (s *Service) GetActiveRideForDriver(ctx context.Context, driverUserID string) (*Ride, error) {
	profile, err := s.repo.FindDriverProfileByUserID(ctx, driverUserID)
	if err == nil {
		rideID, redisErr := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if redisErr == nil && rideID != "" {
			return s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
		}
	}
	return s.repo.FindActiveByDriver(ctx, driverUserID)
}

// refundDriverCredit returns the agreed-fare credit to a driver after a
// blameless cancellation. Best-effort: a refund failure is logged, never fatal.
func (s *Service) refundDriverCredit(ctx context.Context, rideID string, driverProfileID *string, why string) {
	if s.packages == nil || driverProfileID == nil {
		return
	}
	driverUserID, err := s.repo.FindDriverUserIDByProfileID(ctx, *driverProfileID)
	if err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Msg("ride: credit refund — could not resolve driver user")
		return
	}
	if err := s.packages.RefundCredit(ctx, driverUserID); err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Str("driver_user_id", driverUserID).Msg("ride: credit refund failed")
		return
	}
	s.log.Info().Str("ride_id", rideID).Str("driver_user_id", driverUserID).Str("reason", why).Msg("ride: credit refunded")
}

// recordCancelPenalty escalates a fault cancellation by the given party.
//   - increments their daily cancel counter (resets end-of-day)
//   - at warnAt → a warning is published
//   - at banAt  → a 24h temp-ban, OR an indefinite suspension once the user has
//     accumulated PenaltyBansBeforeSuspend lifetime bans
//
// role is "DRIVER" or "CUSTOMER"; userID is the user's id (for a driver this is
// the user_id, not the profile_id). Best-effort — never blocks the cancel.
func (s *Service) recordCancelPenalty(ctx context.Context, userID, role string) {
	warnAt, banAt := s.cfg.Customer.CancelWarnThreshold, s.cfg.Customer.CancelBanThreshold
	dailyKey := rkeys.K.CustomerDailyCancel(userID)
	if role == "DRIVER" {
		warnAt, banAt = s.cfg.Driver.CancelWarnThreshold, s.cfg.Driver.CancelBanThreshold
		dailyKey = rkeys.K.DriverDailyCancel(userID)
	}

	count, err := s.redis.Incr(ctx, dailyKey).Result()
	if err != nil {
		return
	}
	s.redis.ExpireAt(ctx, dailyKey, endOfDay())
	n := int(count)

	if warnAt > 0 && n == warnAt {
		s.analytics.Publish(ctx, "user.cancel_warned", role, userID, nil, map[string]interface{}{
			"daily_cancels": n, "role": role,
		})
		s.log.Info().Str("user_id", userID).Str("role", role).Int("daily_cancels", n).Msg("penalty: cancellation warning")
	}

	// Apply the penalty exactly once, at the moment the count crosses banAt.
	if banAt > 0 && n == banAt {
		bans, err := s.repo.IncrementUserBanCount(ctx, userID)
		if err != nil {
			s.log.Warn().Err(err).Str("user_id", userID).Msg("penalty: failed to increment ban count")
			return
		}
		if bans >= s.cfg.Penalty.BansBeforeSuspend {
			_ = s.repo.SuspendUserIndefinitely(ctx, userID, "excessive_cancellations")
			s.analytics.Publish(ctx, "user.suspended", role, userID, nil, map[string]interface{}{
				"reason": "excessive_cancellations", "ban_count": bans, "role": role,
			})
			s.log.Warn().Str("user_id", userID).Str("role", role).Int("ban_count", bans).Msg("penalty: user SUSPENDED (max bans reached)")
		} else {
			until := time.Now().Add(time.Duration(s.cfg.Penalty.BanHours) * time.Hour)
			_ = s.repo.BanUserUntil(ctx, userID, until, "excessive_cancellations")
			s.analytics.Publish(ctx, "user.banned", role, userID, nil, map[string]interface{}{
				"reason": "excessive_cancellations", "ban_count": bans, "ban_hours": s.cfg.Penalty.BanHours, "role": role,
			})
			s.log.Warn().Str("user_id", userID).Str("role", role).Int("ban_count", bans).Int("hours", s.cfg.Penalty.BanHours).Msg("penalty: user TEMP-BANNED")
		}
		// Kick active sessions so the ban applies immediately (the middleware
		// rejects requests whose session key is gone).
		s.revokeUserSessions(ctx, userID)
	}
}

// revokeUserSessions deletes every active session key for a user so a ban or
// suspension takes effect at once rather than on next token refresh.
func (s *Service) revokeUserSessions(ctx context.Context, userID string) {
	iter := s.redis.Scan(ctx, 0, "session:"+userID+":*", 100).Iterator()
	for iter.Next(ctx) {
		s.redis.Del(ctx, iter.Val())
	}
}

// creditCharged reports whether the ride had reached fare agreement — the
// point at which the driver's credit was deducted.
func creditCharged(status Status) bool {
	switch status {
	case StatusConfirmed, StatusDriverEnRoute, StatusDriverArrived, StatusInProgress:
		return true
	}
	return false
}

// ChargeForAgreedFare deducts one ride credit when a fare is agreed (the ride
// reaches CONFIRMED via negotiation Accept or a manual fare lock). Charging at
// agreement — not at completion — is what closes the "do the trip then never
// tap Complete" loophole: the driver is already committed the moment a deal
// exists. Idempotent per ride via a Redis guard so Accept + manual-lock can't
// double-charge. Best-effort: a failure is logged, never blocks the ride.
func (s *Service) ChargeForAgreedFare(ctx context.Context, rideID string) {
	if s.packages == nil {
		return
	}
	ok, err := s.redis.SetNX(ctx, "ride:credit_charged:"+rideID, "1", 24*time.Hour).Result()
	if err != nil || !ok {
		return // redis error, or already charged for this ride
	}
	r, err := s.repo.FindByID(ctx, rideID)
	if err != nil || r.DriverID == nil {
		return
	}
	driverUserID, err := s.repo.FindDriverUserIDByProfileID(ctx, *r.DriverID)
	if err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Msg("ride: credit charge — could not resolve driver user")
		return
	}
	if err := s.packages.DeductCredit(ctx, driverUserID); err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Str("driver_user_id", driverUserID).Msg("ride: credit charge on agreement failed")
		return
	}
	s.log.Info().Str("ride_id", rideID).Str("driver_user_id", driverUserID).Msg("ride: credit charged on fare agreement")
}

// FinalizeStaleInProgressRides auto-completes rides stuck IN_PROGRESS past
// cfg.Ride.MaxInProgressMinutes — a driver started a trip but never completed
// it (went offline, killed the app). Without this the ride lingers forever as a
// ghost AND keeps the driver locked ON_TRIP, unable to take new work. The credit
// was charged at fare agreement, so there is nothing to charge or refund here —
// we just settle the final fare and release both parties. Returns how many were
// finalized. Designed to be called on a periodic background tick.
func (s *Service) FinalizeStaleInProgressRides(ctx context.Context) (int, error) {
	stale, err := s.repo.FindStaleInProgress(ctx, s.cfg.Ride.MaxInProgressMinutes)
	if err != nil {
		return 0, err
	}
	finalized := 0
	for _, r := range stale {
		// Atomic transition — skip if the ride changed underneath us.
		if err := s.repo.Transition(ctx, r.ID, StatusInProgress, StatusCompleted); err != nil {
			continue
		}
		_ = s.repo.SetCompleted(ctx, r.ID)
		_ = s.repo.SetFinalFare(ctx, r.ID, r.AgreedFare, 0, 0, false, 0)
		_ = s.repo.IncrementDriverRides(ctx, r.DriverUserID)
		_ = s.repo.AppendEvent(ctx, r.ID, "ride.auto_finalized", "SYSTEM", r.ID, map[string]interface{}{
			"reason":      "in_progress_timeout",
			"max_minutes": s.cfg.Ride.MaxInProgressMinutes,
		})
		s.analytics.Publish(ctx, "ride.auto_finalized", "SYSTEM", r.ID, &r.ID, map[string]interface{}{
			"ride_id": r.ID, "reason": "in_progress_timeout",
		})
		// Release the driver (clears ON_TRIP, re-adds to geo) and the customer's
		// active-ride pointer so both can move on.
		profileID := r.DriverProfileID
		s.releaseRideRedisState(ctx, r.ID, r.CustomerID, &profileID, r.TransportType)
		s.hub.SendToCustomer(r.ID, tracking.Message{
			Type: "ride_completed", RideID: r.ID,
			Payload: map[string]interface{}{"final_fare": r.AgreedFare, "auto": true},
		})
		s.hub.SendToDriver(r.DriverProfileID, tracking.Message{
			Type: "ride_completed", RideID: r.ID,
			Payload: map[string]interface{}{"reason": "Ride auto-finalized after timeout.", "auto": true},
		})
		s.log.Warn().Str("ride_id", r.ID).Str("driver_profile_id", r.DriverProfileID).Msg("ride: auto-finalized stale IN_PROGRESS ride")
		finalized++
	}
	return finalized, nil
}

// CancelRide cancels a ride initiated by a customer.
func (s *Service) CancelRide(ctx context.Context, rideID, customerID, reason string) error {
	r, err := s.repo.FindByIDAndCustomer(ctx, rideID, customerID)
	if err != nil {
		return err
	}
	// Already in a terminal state — treat cancel as idempotent success.
	// This handles the common case where the matching engine or a timeout
	// cancelled the ride between when the customer tapped "Cancel" and when
	// the request reached the server.
	if IsTerminal(r.Status) {
		return nil
	}
	if !CancellableStatuses[r.Status] {
		return apperrors.ErrInvalidTransition
	}
	cancellationFee := 0.0
	if r.Status == StatusDriverArrived && r.PricingConfigID != nil && s.fareRepo != nil {
		cfg, ferr := s.fareRepo.GetConfigByID(ctx, *r.PricingConfigID)
		if ferr == nil {
			cancellationFee = fare.CancellationFee(cfg, true)
		}
	}

	didCancel, err := s.repo.CancelWithFee(ctx, rideID, reason, "CUSTOMER", cancellationFee)
	if err != nil {
		return err
	}
	if !didCancel {
		return nil // already terminal between read and write — idempotent
	}

	// The customer cancelled after the fare was agreed → the driver is blameless,
	// so refund the credit charged at agreement.
	if creditCharged(r.Status) {
		s.refundDriverCredit(ctx, rideID, r.DriverID, "customer_cancelled")
	}

	s.releaseRideRedisState(ctx, rideID, customerID, r.DriverID, r.TransportType)

	_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "CUSTOMER", customerID, map[string]interface{}{
		"reason": reason, "status_at_cancel": string(r.Status),
	})

	// Escalating cancellation penalty (warn → 24h ban → suspension).
	s.recordCancelPenalty(ctx, customerID, "CUSTOMER")
	s.analytics.Publish(ctx, "ride.cancelled", "CUSTOMER", customerID, &rideID, map[string]interface{}{
		"ride_id": rideID, "status_at_cancel": string(r.Status),
		"cancelled_by_role": "CUSTOMER", "reason": reason,
	})
	s.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_cancelled", RideID: rideID,
		Payload: map[string]interface{}{"reason": reason},
	})
	// Notify the assigned driver so they stop navigating to a cancelled pickup.
	if r.DriverID != nil {
		s.hub.SendToDriver(*r.DriverID, tracking.Message{
			Type:    "ride_cancelled",
			RideID:  rideID,
			Payload: map[string]interface{}{"reason": "Customer cancelled the ride."},
		})
	}
	return nil
}

// StartNegotiationTimeout starts a 5-minute inactivity timer that auto-cancels
// a ride still in NEGOTIATING state. The timer is stored in negTimers so it can
// be reset (on each counter-offer) or cancelled (on fare acceptance).
func (s *Service) StartNegotiationTimeout(rideID string) {
	fire := func() {
		s.negTimers.Delete(rideID)
		ctx := context.Background()
		r, err := s.repo.FindByID(ctx, rideID)
		if err != nil || r.Status != StatusNegotiating {
			return
		}
		s.log.Warn().Str("ride_id", rideID).Msg("ride: negotiation timeout — cancelling")
		// NEGOTIATING — no credit was charged yet (charge happens at agreement), so no refund.
		_, _ = s.repo.Cancel(ctx, rideID, "negotiation_timeout", "SYSTEM")
		s.releaseRideRedisState(ctx, rideID, r.CustomerID, r.DriverID, r.TransportType)
		_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "SYSTEM", rideID, map[string]interface{}{
			"reason": "negotiation_timeout",
		})
		s.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", rideID, &rideID, map[string]interface{}{
			"ride_id": rideID, "reason": "negotiation_timeout",
		})
		s.hub.SendToCustomer(rideID, tracking.Message{
			Type: "ride_cancelled", RideID: rideID,
			Payload: map[string]interface{}{"reason": "Negotiation timed out. Please request a new ride."},
		})
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, tracking.Message{
				Type: "ride_cancelled", RideID: rideID,
				Payload: map[string]interface{}{"reason": "Negotiation timed out."},
			})
		}
	}

	t := time.AfterFunc(negotiationTimeoutDuration, fire)
	s.negTimers.Store(rideID, t)
}

// ResetNegotiationTimeout resets the inactivity clock back to the full 5 minutes.
// Called by negotiation.Service on every counter-offer so the clock only runs
// during true silence, not while the parties are actively negotiating.
func (s *Service) ResetNegotiationTimeout(rideID string) {
	if v, ok := s.negTimers.Load(rideID); ok {
		t := v.(*time.Timer)
		// Stop the timer; drain the channel if it already fired between Load and Stop.
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(negotiationTimeoutDuration)
	}
}

// CancelNegotiationTimeout disarms the inactivity timer without cancelling the ride.
// Called when negotiation ends cleanly (fare accepted or ride manually cancelled).
func (s *Service) CancelNegotiationTimeout(rideID string) {
	if v, ok := s.negTimers.Load(rideID); ok {
		t := v.(*time.Timer)
		t.Stop()
		select {
		case <-t.C:
		default:
		}
		s.negTimers.Delete(rideID)
	}
}

// SetEnRoute transitions CONFIRMED → DRIVER_EN_ROUTE.
func (s *Service) SetEnRoute(ctx context.Context, rideID, driverUserID string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if err := ValidateTransition(r.Status, StatusDriverEnRoute); err != nil {
		return err
	}
	if err := s.repo.Transition(ctx, rideID, StatusConfirmed, StatusDriverEnRoute); err != nil {
		return err
	}
	s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusDriverEnRoute), 0)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.driver_en_route", "DRIVER", driverUserID, nil)
	s.analytics.Publish(ctx, "ride.driver_en_route", "DRIVER", driverUserID, &rideID, map[string]interface{}{"ride_id": rideID})
	s.hub.SendToCustomer(rideID, tracking.Message{Type: "driver_en_route", RideID: rideID})
	return nil
}

// MarkDriverArrived transitions DRIVER_EN_ROUTE → DRIVER_ARRIVED (server geofence).
func (s *Service) MarkDriverArrived(ctx context.Context, rideID, driverProfileID string) error {
	if err := s.repo.Transition(ctx, rideID, StatusDriverEnRoute, StatusDriverArrived); err != nil {
		return err
	}
	if err := s.repo.SetDriverArrived(ctx, rideID); err != nil {
		return err
	}
	s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusDriverArrived), 0)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.driver_arrived", "SYSTEM", rideID, nil)
	s.analytics.Publish(ctx, "ride.driver_arrived", "SYSTEM", rideID, &rideID, nil)
	s.hub.SendToCustomer(rideID, tracking.Message{Type: "driver_arrived", RideID: rideID})
	s.startPickupExpiryTimer(rideID)
	return nil
}

// SetDriverArrived transitions DRIVER_EN_ROUTE -> DRIVER_ARRIVED from the driver app.
// If the ride is still CONFIRMED (en-route call was in-flight or skipped), it
// automatically advances through DRIVER_EN_ROUTE first so the driver is never
// blocked by a stale state on their device.
func (s *Service) SetDriverArrived(ctx context.Context, rideID, driverUserID string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}

	// Auto-advance through CONFIRMED → DRIVER_EN_ROUTE if needed.
	if r.Status == StatusConfirmed {
		if err := s.repo.Transition(ctx, rideID, StatusConfirmed, StatusDriverEnRoute); err != nil {
			return err
		}
		s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusDriverEnRoute), 0)
		_ = s.repo.AppendEvent(ctx, rideID, "ride.driver_en_route", "DRIVER", driverUserID, nil)
		s.hub.SendToCustomer(rideID, tracking.Message{Type: "driver_en_route", RideID: rideID})
		r.Status = StatusDriverEnRoute
	}

	if err := ValidateTransition(r.Status, StatusDriverArrived); err != nil {
		return err
	}
	within, err := s.withinRadius(ctx, driverUserID, r.DriverID, r.PickupPoint, s.cfg.Ride.StartRadiusM)
	if err != nil {
		return fmt.Errorf("geo-gate check: %w", err)
	}
	if !within {
		return apperrors.ErrGeoFence
	}
	if err := s.repo.Transition(ctx, rideID, StatusDriverEnRoute, StatusDriverArrived); err != nil {
		return err
	}
	if err := s.repo.SetDriverArrived(ctx, rideID); err != nil {
		return err
	}
	s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusDriverArrived), 0)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.driver_arrived", "DRIVER", driverUserID, nil)
	s.analytics.Publish(ctx, "ride.driver_arrived", "DRIVER", driverUserID, &rideID, map[string]interface{}{"ride_id": rideID})
	s.hub.SendToCustomer(rideID, tracking.Message{Type: "driver_arrived", RideID: rideID})
	s.startPickupExpiryTimer(rideID)
	return nil
}

// startPickupExpiryTimer fires after 5 minutes if customer hasn't boarded.
func (s *Service) startPickupExpiryTimer(rideID string) {
	time.AfterFunc(pickupExpiryDuration, func() {
		ctx := context.Background()
		r, err := s.repo.FindByID(ctx, rideID)
		if err != nil || r.Status != StatusDriverArrived {
			return
		}
		_ = s.repo.SetPickupExpired(ctx, rideID)
		s.hub.SendToCustomer(rideID, tracking.Message{
			Type: "ride_pickup_expired", RideID: rideID,
			Payload: map[string]interface{}{"message": "Driver has been waiting 5 minutes. You may cancel."},
		})
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, tracking.Message{
				Type: "ride_pickup_expired", RideID: rideID,
				Payload: map[string]interface{}{"message": "Customer has not arrived. You may cancel."},
			})
		}
	})
}

// CancelAfterPickupExpiry lets a driver cancel after the pickup wait window without decline penalties.
func (s *Service) CancelAfterPickupExpiry(ctx context.Context, rideID, driverUserID string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if r.Status != StatusDriverArrived || !r.PickupExpired {
		return apperrors.New(409, "PICKUP_NOT_EXPIRED", "pickup wait window has not expired")
	}

	// ── GPS-verify the no-show ────────────────────────────────────────────────
	// A genuine no-show means the driver waited at the pickup and left empty, so
	// their last-known position should still be near the pickup. If they've
	// driven off (e.g. carried the passenger to the destination, then claim
	// no-show to dodge the credit), deny the refund and flag the ride. Dev
	// bypass mirrors the geofence skip.
	noShowVerified := s.cfg.Ride.DevSkipGeofence
	var driverDistM float64 = -1
	if !noShowVerified {
		if pt, ok := s.driverLastKnownPoint(ctx, driverUserID, r.DriverID); ok {
			driverDistM = geo.DistanceKM(pt, r.PickupPoint) * 1000
			noShowVerified = driverDistM <= float64(s.cfg.Ride.NoShowVerifyRadiusM)
		}
		// No location at all → cannot verify → not verified (no refund, flagged).
	}

	cancelReason := "customer_no_show"
	if !noShowVerified {
		cancelReason = "customer_no_show_unverified"
	}
	didCancel, err := s.repo.Cancel(ctx, rideID, cancelReason, "DRIVER")
	if err != nil {
		return err
	}
	if !didCancel {
		return nil
	}

	if creditCharged(r.Status) {
		if noShowVerified {
			// Driver still at the pickup + wait window expired → blameless → refund
			// the credit charged at agreement.
			s.refundDriverCredit(ctx, rideID, r.DriverID, "customer_no_show")
		} else {
			// Driver had driven away (or no GPS) → suspicious. Keep the credit
			// forfeited and flag for review.
			s.log.Warn().
				Str("ride_id", rideID).Str("driver_user_id", driverUserID).
				Float64("driver_dist_m", driverDistM).Int("allowed_m", s.cfg.Ride.NoShowVerifyRadiusM).
				Msg("ride: no-show refund DENIED — driver not at pickup (possible fraud)")
			_ = s.repo.AppendEvent(ctx, rideID, "ride.no_show_unverified", "SYSTEM", rideID, map[string]interface{}{
				"reason": "driver_not_at_pickup", "driver_dist_m": driverDistM,
			})
			s.analytics.Publish(ctx, "ride.no_show_unverified", "SYSTEM", driverUserID, &rideID, map[string]interface{}{
				"ride_id": rideID, "driver_dist_m": driverDistM,
			})
		}
	}
	s.releaseRideRedisState(ctx, rideID, r.CustomerID, r.DriverID, r.TransportType)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "DRIVER", driverUserID, map[string]interface{}{
		"reason": cancelReason, "pickup_expired": true, "no_show_verified": noShowVerified,
	})
	s.analytics.Publish(ctx, "ride.cancelled", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id": rideID, "reason": cancelReason, "pickup_expired": true,
	})
	s.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_cancelled", RideID: rideID,
		Payload: map[string]interface{}{"reason": "Customer no-show after pickup wait window."},
	})
	return nil
}

// StartRide transitions DRIVER_ARRIVED → IN_PROGRESS.
func (s *Service) StartRide(ctx context.Context, rideID, driverUserID string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if err := ValidateTransition(r.Status, StatusInProgress); err != nil {
		return err
	}
	within, err := s.withinRadius(ctx, driverUserID, r.DriverID, r.PickupPoint, s.cfg.Ride.StartRadiusM)
	if err != nil {
		return fmt.Errorf("geo-gate check: %w", err)
	}
	if !within {
		return apperrors.ErrGeoFence
	}
	if err := s.repo.Transition(ctx, rideID, StatusDriverArrived, StatusInProgress); err != nil {
		return err
	}
	if err := s.repo.SetStarted(ctx, rideID); err != nil {
		return err
	}
	// 2-hour TTL: if CompleteRide's Redis write fails (the exact bug that caused
	// the "stuck IN_PROGRESS" ghost ride), this key self-heals within 2 hours
	// instead of living forever with no TTL.
	if err := s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusInProgress), 2*time.Hour).Err(); err != nil {
		s.log.Error().Err(err).Str("ride_id", rideID).Msg("ride: failed to write IN_PROGRESS state to Redis")
	}
	_ = s.repo.AppendEvent(ctx, rideID, "ride.started", "DRIVER", driverUserID, nil)
	s.analytics.Publish(ctx, "ride.started", "DRIVER", driverUserID, &rideID, map[string]interface{}{"ride_id": rideID})
	// Notify the customer that the journey has started so they transition to
	// in_progress immediately — without this the customer is stuck on the
	// "arrived" screen until the 30-second polling fallback fires.
	s.hub.SendToCustomer(rideID, tracking.Message{Type: "ride_started", RideID: rideID})
	return nil
}

// CompleteRide transitions IN_PROGRESS → COMPLETED.
func (s *Service) CompleteRide(ctx context.Context, rideID, driverUserID string, finalDest *geo.Point, finalDestAddress *string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if err := ValidateTransition(r.Status, StatusCompleted); err != nil {
		return err
	}
	destination := r.DestinationPoint
	if finalDest != nil {
		if err := finalDest.Validate(); err != nil {
			return err
		}
		if err := s.repo.SetCompletionDestination(ctx, rideID, *finalDest, finalDestAddress); err != nil {
			return err
		}
		destination = *finalDest
		r.DestinationPoint = *finalDest
		if finalDestAddress != nil {
			r.DestinationAddress = *finalDestAddress
		}
	}

	within, err := s.withinRadius(ctx, driverUserID, r.DriverID, destination, s.cfg.Ride.CompleteRadiusM)
	if err != nil {
		return fmt.Errorf("geo-gate check: %w", err)
	}
	if !within {
		return apperrors.ErrGeoFence
	}
	if err := s.repo.Transition(ctx, rideID, StatusInProgress, StatusCompleted); err != nil {
		return err
	}
	if err := s.repo.SetCompleted(ctx, rideID); err != nil {
		return err
	}

	waitingSeconds := 0
	if r.DriverArrivedAt != nil && r.StartedAt != nil {
		waitingSeconds = int(r.StartedAt.Sub(*r.DriverArrivedAt).Seconds())
		if waitingSeconds < 0 {
			waitingSeconds = 0
		}
	}
	if waitingSeconds > 120*60 {
		waitingSeconds = 120 * 60
	}

	var finalFare *float64
	waitingCharge := 0.0
	nightApplied := false
	nightPct := 0.0
	if s.fareRepo != nil && r.PricingConfigID != nil && r.StartedAt != nil {
		pricingCfg, err := s.fareRepo.GetConfigByID(ctx, *r.PricingConfigID)
		if err != nil {
			s.log.Warn().Err(err).Str("ride_id", rideID).Msg("ride: could not load pricing config for final fare")
		} else {
			distKM := 0.0
			if r.EstimatedDistanceKM != nil {
				distKM = *r.EstimatedDistanceKM
			}
			b := fare.Calculate(pricingCfg, distKM, *r.StartedAt, waitingSeconds)
			waitingCharge = b.WaitingCharge
			nightApplied = b.NightApplied
			nightPct = pricingCfg.NightSurchargePct
			if r.AgreedFare != nil {
				f := *r.AgreedFare + waitingCharge
				finalFare = &f
			} else {
				f := b.TotalFare
				finalFare = &f
			}
		}
	} else if r.AgreedFare != nil {
		finalFare = r.AgreedFare
	}
	if err := s.repo.SetFinalFare(ctx, rideID, finalFare, waitingSeconds, waitingCharge, nightApplied, nightPct); err != nil {
		return err
	}
	_ = s.repo.IncrementDriverRides(ctx, driverUserID)
	if s.routes != nil && r.AgreedFare != nil {
		s.routes.RecordAgreedFare(ctx, r.PickupPoint.Lat, r.PickupPoint.Lng, destination.Lat, destination.Lng, r.TransportType, *r.AgreedFare)
	}

	// Note: the ride credit was charged when the fare was agreed (negotiation
	// Accept / manual lock → CONFIRMED), not here — completing must never
	// double-charge, and charging at agreement closes the "finish the trip but
	// never tap Complete" free-ride loophole.

	profile, _ := s.repo.FindDriverProfileByUserID(ctx, driverUserID)
	if profile != nil {
		s.releaseDriverRedisState(ctx, profile.ID, profile.TransportType)
	}
	s.redis.Del(ctx, rkeys.K.CustomerActiveRide(r.CustomerID))
	// Keep RideState alive as COMPLETED for 5 minutes so a customer WS
	// reconnect (e.g. brief signal drop at ride end) still receives the
	// state-replay message and auto-navigates to the rating screen.
	// The key expires automatically — no manual cleanup required.
	if err := s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusCompleted), 5*time.Minute).Err(); err != nil {
		s.log.Warn().Err(err).Str("ride_id", rideID).Msg("ride: failed to write COMPLETED state to Redis (non-fatal)")
	}

	_ = s.repo.AppendEvent(ctx, rideID, "ride.completed", "DRIVER", driverUserID, nil)
	s.analytics.Publish(ctx, "ride.completed", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id": rideID, "agreed_fare": r.AgreedFare,
	})
	s.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_completed", RideID: rideID,
		Payload: map[string]interface{}{
			"agreed_fare":     r.AgreedFare,
			"waiting_seconds": waitingSeconds,
			"waiting_charge":  waitingCharge,
			"night_applied":   nightApplied,
			"final_fare":      finalFare,
		},
	})
	return nil
}

// releaseRideRedisState cleans up all Redis keys for a cancelled/completed ride.
func (s *Service) releaseRideRedisState(ctx context.Context, rideID, customerID string, driverProfileID *string, vehicleType string) {
	s.redis.Del(ctx, rkeys.K.CustomerActiveRide(customerID))
	s.redis.Del(ctx, rkeys.K.RideState(rideID))
	s.redis.Del(ctx, rkeys.K.RidePendingDriver(rideID))
	s.redis.Del(ctx, rkeys.K.RideExcludedDrivers(rideID))
	if driverProfileID != nil {
		s.releaseDriverRedisState(ctx, *driverProfileID, vehicleType)
	}
}

func (s *Service) releaseDriverRedisState(ctx context.Context, driverProfileID, vehicleType string) {
	s.redis.Set(ctx, rkeys.K.DriverState(driverProfileID), driverStateAvailable, 0)
	s.redis.Del(ctx, rkeys.K.DriverActiveRide(driverProfileID))

	// Re-add to GEO index using last known location
	locJSON, err := s.redis.Get(ctx, rkeys.K.DriverLocation(driverProfileID)).Result()
	if err == nil {
		var loc struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		}
		if json.Unmarshal([]byte(locJSON), &loc) == nil && (loc.Lat != 0 || loc.Lng != 0) {
			s.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(vehicleType), &goredis.GeoLocation{
				Name:      driverProfileID,
				Longitude: loc.Lng,
				Latitude:  loc.Lat,
			})
		}
	}
}

// DriverCancelRide lets a driver cancel a ride they have accepted.
// Valid from CONFIRMED, DRIVER_EN_ROUTE, or DRIVER_ARRIVED (with or without
// pickup expiry). The customer is notified via WS immediately.
// Cancelling after confirming is tracked in analytics for driver-penalty scoring.
func (s *Service) DriverCancelRide(ctx context.Context, rideID, driverUserID, reason string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	// Idempotent: if already terminal, treat as success.
	if IsTerminal(r.Status) {
		return nil
	}
	// Only cancellable from these states — IN_PROGRESS cannot be cancelled
	// (the driver must complete the ride once the customer is aboard).
	validFrom := map[Status]bool{
		StatusConfirmed:     true,
		StatusDriverEnRoute: true,
		StatusDriverArrived: true,
	}
	if !validFrom[r.Status] {
		return apperrors.ErrInvalidTransition
	}
	didCancel, err := s.repo.Cancel(ctx, rideID, reason, "DRIVER")
	if err != nil {
		return err
	}
	if !didCancel {
		return nil
	}
	// Deliberately NO credit refund here: a driver who bails on an agreed ride
	// forfeits the credit. That's the cost that discourages accepting-then-
	// abandoning and nudges drivers to actually complete. The only blameless
	// driver exit is a verified no-show via CancelAfterPickupExpiry.
	// On top of the forfeited credit, count it toward the cancellation penalty
	// (warn → 24h ban → suspension).
	s.recordCancelPenalty(ctx, driverUserID, "DRIVER")
	s.releaseRideRedisState(ctx, rideID, r.CustomerID, r.DriverID, r.TransportType)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "DRIVER", driverUserID, map[string]interface{}{
		"reason": reason, "status_at_cancel": string(r.Status),
	})
	s.analytics.Publish(ctx, "ride.cancelled", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id": rideID, "reason": reason,
		"cancelled_by_role": "DRIVER", "status_at_cancel": string(r.Status),
	})
	// Push to customer immediately so their screen updates without waiting for a poll.
	s.hub.SendToCustomer(rideID, tracking.Message{
		Type:    "ride_cancelled",
		RideID:  rideID,
		Payload: map[string]interface{}{"reason": "Your driver has cancelled the ride."},
	})
	return nil
}

func endOfDay() time.Time { return timeutil.EndOfDay() }

// withinRadius checks whether a driver is within radiusM of target.
//
// It is used for the three geofence gates:
//   - /arrive  — driver must be near the pickup
//   - /start   — driver must still be near the pickup
//   - /complete — driver must be near the destination
//
// Source priority (most accurate first):
//  1. DEV_SKIP_GEOFENCE=true → always pass (dev testing without physical proximity)
//  2. Redis driver:location:<profileID> key — updated every 5 s during a trip,
//     always fresher than the DB write that follows it.
//  3. PostGIS driver_locations — authoritative fallback when Redis key is absent
//     (e.g. cold start, Redis flush).
func (s *Service) withinRadius(ctx context.Context, driverUserID string, driverProfileID *string, target geo.Point, radiusM int) (bool, error) {
	// Dev bypass — mirror of DEV_AUTO_APPROVE_DRIVERS; never true in production.
	if s.cfg.Ride.DevSkipGeofence {
		return true, nil
	}

	// Redis check: freshest location, already stored by UpdateLocation.
	if driverProfileID != nil {
		raw, err := s.redis.Get(ctx, rkeys.K.DriverLocation(*driverProfileID)).Result()
		if err == nil {
			var loc struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			}
			if json.Unmarshal([]byte(raw), &loc) == nil && (loc.Lat != 0 || loc.Lng != 0) {
				distM := geo.DistanceKM(geo.Point{Lat: loc.Lat, Lng: loc.Lng}, target) * 1000
				return distM <= float64(radiusM), nil
			}
		}
	}

	// PostGIS fallback.
	return s.repo.DriverWithinRadius(ctx, driverUserID, target, radiusM)
}

// driverLastKnownPoint returns the driver's freshest known position — Redis
// first (rewritten on every ping), then the PostGIS fallback. ok=false if the
// driver has no recorded location at all.
func (s *Service) driverLastKnownPoint(ctx context.Context, driverUserID string, driverProfileID *string) (geo.Point, bool) {
	if driverProfileID != nil {
		raw, err := s.redis.Get(ctx, rkeys.K.DriverLocation(*driverProfileID)).Result()
		if err == nil {
			var loc struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			}
			if json.Unmarshal([]byte(raw), &loc) == nil && (loc.Lat != 0 || loc.Lng != 0) {
				return geo.Point{Lat: loc.Lat, Lng: loc.Lng}, true
			}
		}
	}
	if p, ok, err := s.repo.DriverLastLocation(ctx, driverUserID); err == nil && ok {
		return p, true
	}
	return geo.Point{}, false
}
