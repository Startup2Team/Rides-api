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

// CreateRide creates a new ride in SEARCHING status and triggers matching.
func (s *Service) CreateRide(ctx context.Context, customerID, transportType, pickupAddr, destAddr string, pickup, dest geo.Point, initialFare *float64) (*Ride, error) {
	key := rkeys.K.CustomerDailyCancel(customerID)
	count, _ := s.redis.Get(ctx, key).Int()
	if count >= s.cfg.Customer.CancelSuspendThreshold {
		return nil, apperrors.ErrCustomerSuspended
	}

	r, err := s.repo.CreateRide(ctx, customerID, transportType, pickupAddr, destAddr, pickup, dest, initialFare)
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

// CancelRide cancels a ride initiated by a customer.
func (s *Service) CancelRide(ctx context.Context, rideID, customerID, reason string) error {
	r, err := s.repo.FindByIDAndCustomer(ctx, rideID, customerID)
	if err != nil {
		return err
	}
	if !CancellableStatuses[r.Status] {
		return apperrors.ErrInvalidTransition
	}
	if err := s.repo.Cancel(ctx, rideID, reason, "CUSTOMER"); err != nil {
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
func (s *Service) SetDriverArrived(ctx context.Context, rideID, driverUserID string) error {
	r, err := s.repo.FindByIDAndDriver(ctx, rideID, driverUserID)
	if err != nil {
		return err
	}
	if err := ValidateTransition(r.Status, StatusDriverArrived); err != nil {
		return err
	}
	within, err := s.repo.DriverWithinRadius(ctx, driverUserID, r.PickupPoint, s.cfg.Ride.StartRadiusM)
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
	within, err := s.repo.DriverWithinRadius(ctx, driverUserID, r.PickupPoint, s.cfg.Ride.StartRadiusM)
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

	within, err := s.repo.DriverWithinRadius(ctx, driverUserID, destination, s.cfg.Ride.CompleteRadiusM)
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
	_ = s.repo.IncrementDriverRides(ctx, driverUserID)
	if s.routes != nil && r.AgreedFare != nil {
		s.routes.RecordAgreedFare(ctx, r.PickupPoint.Lat, r.PickupPoint.Lng, destination.Lat, destination.Lng, r.TransportType, *r.AgreedFare)
	}

	profile, _ := s.repo.FindDriverProfileByUserID(ctx, driverUserID)
	if profile != nil {
		s.releaseDriverRedisState(ctx, profile.ID, profile.TransportType)
	}
	s.redis.Del(ctx, rkeys.K.CustomerActiveRide(r.CustomerID))
	s.redis.Del(ctx, rkeys.K.RideState(rideID))

	_ = s.repo.AppendEvent(ctx, rideID, "ride.completed", "DRIVER", driverUserID, nil)
	s.analytics.Publish(ctx, "ride.completed", "DRIVER", driverUserID, &rideID, map[string]interface{}{
		"ride_id": rideID, "agreed_fare": r.AgreedFare,
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

func endOfDay() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
}
