package ride

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

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

// PackagesService is used to deduct a ride credit when a trip completes.
type PackagesService interface {
	DeductCredit(ctx context.Context, driverUserID string) error
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

	s.redis.Set(ctx, rkeys.K.RideState(r.ID), string(StatusSearching), 0)
	s.redis.Set(ctx, rkeys.K.CustomerActiveRide(customerID), r.ID, 0)

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
		return s.repo.FindActiveByCustomer(ctx, customerID)
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
		cfg, err := s.fareRepo.GetConfigByID(ctx, *r.PricingConfigID)
		if err == nil {
			cancellationFee = fare.CancellationFee(cfg, true)
		}
	}

	if err := s.repo.CancelWithFee(ctx, rideID, reason, "CUSTOMER", cancellationFee); err != nil {
		return err
	}

	s.releaseRideRedisState(ctx, rideID, customerID, r.DriverID, r.TransportType)

	_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "CUSTOMER", customerID, map[string]interface{}{
		"reason": reason, "status_at_cancel": string(r.Status),
	})

	ckey := rkeys.K.CustomerDailyCancel(customerID)
	count, _ := s.redis.Incr(ctx, ckey).Result()
	s.redis.ExpireAt(ctx, ckey, endOfDay())
	if int(count) == s.cfg.Customer.CancelWarnThreshold {
		s.analytics.Publish(ctx, "customer.cancel_warned", "CUSTOMER", customerID, nil, map[string]interface{}{
			"daily_cancel_count": count,
		})
	}
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

// StartNegotiationTimeout starts a 5-minute goroutine that auto-cancels
// a ride still in NEGOTIATING state after the timeout.
func (s *Service) StartNegotiationTimeout(rideID string) {
	time.AfterFunc(negotiationTimeoutDuration, func() {
		ctx := context.Background()
		r, err := s.repo.FindByID(ctx, rideID)
		if err != nil || r.Status != StatusNegotiating {
			return
		}
		s.log.Warn().Str("ride_id", rideID).Msg("ride: negotiation timeout — cancelling")
		_ = s.repo.Cancel(ctx, rideID, "negotiation_timeout", "SYSTEM")
		s.releaseRideRedisState(ctx, rideID, r.CustomerID, r.DriverID, r.TransportType)
		_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "SYSTEM", rideID, map[string]interface{}{
			"reason": "negotiation_timeout",
		})
		s.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", rideID, &rideID, map[string]interface{}{
			"ride_id": rideID, "reason": "negotiation_timeout",
		})
		s.hub.SendToCustomer(rideID, tracking.Message{
			Type: "ride_cancelled", RideID: rideID,
			Payload: map[string]interface{}{"reason": "Negotiation timed out."},
		})
		if r.DriverID != nil {
			s.hub.SendToDriver(*r.DriverID, tracking.Message{
				Type: "ride_cancelled", RideID: rideID,
				Payload: map[string]interface{}{"reason": "Negotiation timed out."},
			})
		}
	})
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
	if err := s.repo.Cancel(ctx, rideID, "customer_no_show", "DRIVER"); err != nil {
		return err
	}
	s.releaseRideRedisState(ctx, rideID, r.CustomerID, r.DriverID, r.TransportType)
	_ = s.repo.AppendEvent(ctx, rideID, "ride.cancelled", "DRIVER", driverUserID, map[string]interface{}{
		"reason": "customer_no_show", "pickup_expired": true,
	})
	s.analytics.Publish(ctx, "ride.cancelled", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id": rideID, "reason": "customer_no_show", "pickup_expired": true,
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
	s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusInProgress), 0)
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

	// Deduct one ride credit — fire-and-forget on error so the completion is never blocked.
	if s.packages != nil {
		if err := s.packages.DeductCredit(ctx, driverUserID); err != nil {
			s.log.Warn().Err(err).Str("driver_id", driverUserID).Msg("ride: credit deduction failed on complete")
		}
	}

	profile, _ := s.repo.FindDriverProfileByUserID(ctx, driverUserID)
	if profile != nil {
		s.releaseDriverRedisState(ctx, profile.ID, profile.TransportType)
	}
	s.redis.Del(ctx, rkeys.K.CustomerActiveRide(r.CustomerID))
	// Keep RideState alive as COMPLETED for 5 minutes so a customer WS
	// reconnect (e.g. brief signal drop at ride end) still receives the
	// state-replay message and auto-navigates to the rating screen.
	// The key expires automatically — no manual cleanup required.
	s.redis.Set(ctx, rkeys.K.RideState(rideID), string(StatusCompleted), 5*time.Minute)

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
	if err := s.repo.Cancel(ctx, rideID, reason, "DRIVER"); err != nil {
		return err
	}
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

func endOfDay() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
}

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
