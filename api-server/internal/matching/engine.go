package matching

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/notification"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/tracking"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const (
	driverStateAvailable = "AVAILABLE"
	driverStateOnTrip    = "ON_TRIP"
	matchLockTTL         = 20 * time.Second
)

// rideServiceInterface exposes only what the engine needs from ride.Service.
type rideServiceInterface interface {
	StartNegotiationTimeout(rideID string)
}

// candidate is an enriched driver result from the GEO search.
type candidate struct {
	profileID      string
	userID         string
	vehicleType    string
	fcmToken       *string
	distanceM      float64
	dailyDeclines  int
	acceptanceRate float64
	score          float64
}

// Engine orchestrates driver matching for a ride.
type Engine struct {
	rideRepo   *ride.Repository
	driverRepo *driver.Repository
	redis      *goredis.Client
	notify     *notification.Service
	analytics  *analytics.Service
	hub        *tracking.Hub
	cfg        *config.Config
	log        zerolog.Logger
	rideSvc    rideServiceInterface

	// acceptChannels maps rideID → chan bool
	acceptChannels sync.Map
}

func NewEngine(
	rideRepo *ride.Repository,
	driverRepo *driver.Repository,
	rdb *goredis.Client,
	notify *notification.Service,
	ana *analytics.Service,
	hub *tracking.Hub,
	cfg *config.Config,
	log zerolog.Logger,
	rideSvc rideServiceInterface,
) *Engine {
	return &Engine{
		rideRepo:   rideRepo,
		driverRepo: driverRepo,
		redis:      rdb,
		notify:     notify,
		analytics:  ana,
		hub:        hub,
		cfg:        cfg,
		log:        log,
		rideSvc:    rideSvc,
	}
}

// StartSearch kicks off the matching loop for a new ride in a goroutine.
func (e *Engine) StartSearch(rideID string, pickup geo.Point, transportType string) {
	go e.runLoop(context.Background(), rideID, pickup, transportType)
}

// NotifyAccept is called by the driver accept/decline handler.
func (e *Engine) NotifyAccept(rideID string, accepted bool) bool {
	if ch, ok := e.acceptChannels.Load(rideID); ok {
		select {
		case ch.(chan bool) <- accepted:
			return true
		default:
		}
	}
	return false
}

// ValidateAcceptTTL checks that the pending_driver key in Redis still exists.
func (e *Engine) ValidateAcceptTTL(ctx context.Context, rideID string) (string, bool) {
	driverID, err := e.redis.Get(ctx, rkeys.K.RidePendingDriver(rideID)).Result()
	if err != nil {
		return "", false
	}
	return driverID, true
}

// ──────────────────────────────────────────────────────────────────────────
// Internal matching loop
// ──────────────────────────────────────────────────────────────────────────

func (e *Engine) runLoop(ctx context.Context, rideID string, pickup geo.Point, transportType string) {
	maxAttempts := e.cfg.Matching.MaxAttempts
	tried := make(map[string]bool)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidates, err := e.searchCandidates(ctx, pickup, transportType, tried)
		if err != nil || len(candidates) == 0 {
			break
		}

		for _, c := range candidates {
			if tried[c.profileID] {
				continue
			}
			// Skip seeded/offline drivers with no live socket — otherwise each offer waits
			// for the full match timeout before trying the next candidate.
			if !e.hub.IsDriverConnected(c.userID) {
				continue
			}
			tried[c.profileID] = true

			accepted, ok := e.offerToDriver(ctx, rideID, c)
			if !ok {
				continue
			}
			if accepted {
				e.onAccepted(ctx, rideID, c)
				return
			}
			e.onDeclined(ctx, rideID, c)
		}
	}

	// All attempts exhausted
	e.log.Warn().Str("ride_id", rideID).Msg("matching: no driver found — cancelling ride")
	_ = e.rideRepo.Cancel(ctx, rideID, "no driver found after max attempts", "SYSTEM")
	_ = e.rideRepo.AppendEvent(ctx, rideID, "ride.cancelled", "SYSTEM", rideID, map[string]interface{}{
		"reason": "no_driver_found",
	})
	e.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", rideID, &rideID, map[string]interface{}{
		"ride_id": rideID, "reason": "no_driver_found",
	})
	e.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_cancelled", RideID: rideID,
		Payload: map[string]interface{}{"reason": "No driver found nearby. Please try again."},
	})
}

// searchCandidates uses Redis GEO to find nearby drivers, enriches and scores them.
func (e *Engine) searchCandidates(ctx context.Context, pickup geo.Point, vehicleType string, tried map[string]bool) ([]*candidate, error) {
	geoKey := rkeys.K.DriverGeoIndex(vehicleType)

	results, err := e.redis.GeoSearchLocation(ctx, geoKey, &goredis.GeoSearchLocationQuery{
		GeoSearchQuery: goredis.GeoSearchQuery{
			Longitude:  pickup.Lng,
			Latitude:   pickup.Lat,
			Radius:     float64(e.cfg.Matching.ExpandedRadiusM) / 1000.0,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      10,
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()

	if err != nil || len(results) == 0 {
		return e.fallbackPostGIS(ctx, pickup, vehicleType, tried)
	}

	var candidates []*candidate
	for _, r := range results {
		profileID := r.Name
		if tried[profileID] {
			continue
		}

		state, _ := e.redis.Get(ctx, rkeys.K.DriverState(profileID)).Result()
		if state != driverStateAvailable {
			continue
		}

		profile, err := e.driverRepo.FindProfileByID(ctx, profileID)
		if err != nil {
			continue
		}

		declines := 0
		if d, err := e.redis.Get(ctx, rkeys.K.DriverDailyDeclines(profileID)).Int(); err == nil {
			declines = d
		}

		distM := r.Dist * 1000
		normalizedDist := distM / float64(e.cfg.Matching.ExpandedRadiusM)
		normalizedDeclines := math.Min(float64(declines), 10) / 10.0
		acceptancePenalty := 1.0 - profile.AcceptanceRate/100.0
		score := (normalizedDist * 0.6) + (normalizedDeclines * 0.25) + (acceptancePenalty * 0.15)

		candidates = append(candidates, &candidate{
			profileID:      profileID,
			userID:         profile.UserID,
			vehicleType:    profile.TransportType,
			fcmToken:       profile.FCMToken,
			distanceM:      distM,
			dailyDeclines:  declines,
			acceptanceRate: profile.AcceptanceRate,
			score:          score,
		})
	}

	sortCandidates(candidates)
	return candidates, nil
}

// fallbackPostGIS is used on cold start when Redis GEO index is empty.
func (e *Engine) fallbackPostGIS(ctx context.Context, pickup geo.Point, vehicleType string, tried map[string]bool) ([]*candidate, error) {
	var excludedIDs []string
	for id := range tried {
		excludedIDs = append(excludedIDs, id)
	}

	nearby, err := e.driverRepo.FindNearby(ctx, pickup, e.cfg.Matching.ExpandedRadiusM, vehicleType, excludedIDs)
	if err != nil {
		return nil, err
	}

	var candidates []*candidate
	for _, n := range nearby {
		declines := 0
		if d, err := e.redis.Get(ctx, rkeys.K.DriverDailyDeclines(n.ProfileID)).Int(); err == nil {
			declines = d
		}

		normalizedDist := n.DistanceM / float64(e.cfg.Matching.ExpandedRadiusM)
		normalizedDeclines := math.Min(float64(declines), 10) / 10.0
		acceptancePenalty := 1.0 - n.AcceptanceRate/100.0
		score := (normalizedDist * 0.6) + (normalizedDeclines * 0.25) + (acceptancePenalty * 0.15)

		candidates = append(candidates, &candidate{
			profileID:      n.ProfileID,
			userID:         n.UserID,
			vehicleType:    n.TransportType,
			fcmToken:       n.FCMToken,
			distanceM:      n.DistanceM,
			dailyDeclines:  declines,
			acceptanceRate: n.AcceptanceRate,
			score:          score,
		})
	}

	sortCandidates(candidates)
	return candidates, nil
}

// offerToDriver locks the driver with SET NX, sends the offer, waits for response.
func (e *Engine) offerToDriver(ctx context.Context, rideID string, c *candidate) (bool, bool) {
	lockKey := rkeys.K.MatchingLock(c.profileID)
	ttl := time.Duration(e.cfg.Matching.TimeoutSeconds) * time.Second

	ok, err := e.redis.SetNX(ctx, lockKey, rideID, matchLockTTL).Result()
	if err != nil || !ok {
		return false, false
	}
	defer e.redis.Del(ctx, lockKey)

	e.redis.Set(ctx, rkeys.K.RidePendingDriver(rideID), c.profileID, ttl)

	if c.fcmToken != nil {
		_ = e.notify.SendRideRequest(ctx, *c.fcmToken, rideID, "", "", c.distanceM)
	}

	payload := map[string]interface{}{
		"ride_id":    rideID,
		"distance_m": c.distanceM,
	}
	if ridePayload, rpErr := e.rideRepo.GetRideRequestPayload(ctx, rideID); rpErr == nil && ridePayload != nil {
		payload["transport_type"] = ridePayload.TransportType
		payload["distance_km"] = ridePayload.DistanceKM
		payload["pickup_lat"] = ridePayload.PickupLat
		payload["pickup_lng"] = ridePayload.PickupLng
		payload["pickup_address"] = ridePayload.PickupAddress
		payload["dest_lat"] = ridePayload.DestinationLat
		payload["dest_lng"] = ridePayload.DestinationLng
		payload["dest_address"] = ridePayload.DestinationAddress
		payload["suggested_fare"] = ridePayload.SuggestedFare
		payload["customer_name"] = ridePayload.CustomerName
		payload["customer_phone"] = ridePayload.CustomerPhone
	}
	e.hub.SendToDriver(c.userID, tracking.Message{
		Type:    "ride_request",
		RideID:  rideID,
		Payload: payload,
	})

	_ = e.rideRepo.AppendEvent(ctx, rideID, "ride.request_sent", "SYSTEM", c.profileID, map[string]interface{}{
		"driver_id":       c.profileID,
		"score":           strconv.FormatFloat(c.score, 'f', 4, 64),
		"daily_declines":  c.dailyDeclines,
		"acceptance_rate": c.acceptanceRate,
	})

	acceptCh := make(chan bool, 1)
	e.acceptChannels.Store(rideID, acceptCh)
	defer e.acceptChannels.Delete(rideID)

	timer := time.NewTimer(ttl)
	defer timer.Stop()

	select {
	case accepted := <-acceptCh:
		return accepted, true
	case <-timer.C:
		e.redis.Del(ctx, rkeys.K.RidePendingDriver(rideID))
		return false, true
	}
}

func (e *Engine) onAccepted(ctx context.Context, rideID string, c *candidate) {
	_ = e.rideRepo.AssignDriver(ctx, rideID, c.profileID)
	_ = e.rideRepo.Transition(ctx, rideID, ride.StatusSearching, ride.StatusMatched)
	_ = e.rideRepo.Transition(ctx, rideID, ride.StatusMatched, ride.StatusNegotiating)

	e.redis.Set(ctx, rkeys.K.DriverState(c.profileID), driverStateOnTrip, 0)
	e.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(c.vehicleType), c.profileID)
	e.redis.Set(ctx, rkeys.K.DriverActiveRide(c.profileID), rideID, 0)
	e.redis.Set(ctx, rkeys.K.RideState(rideID), string(ride.StatusNegotiating), 0)

	_ = e.rideRepo.AppendEvent(ctx, rideID, "ride.matched", "DRIVER", c.profileID, map[string]interface{}{
		"driver_id": c.profileID,
	})
	_ = e.rideRepo.AppendEvent(ctx, rideID, "ride.negotiation_started", "SYSTEM", rideID, nil)
	e.analytics.Publish(ctx, "ride.negotiation_started", "SYSTEM", rideID, &rideID, nil)

	// Start 5-minute negotiation timeout
	e.rideSvc.StartNegotiationTimeout(rideID)

	e.notifyCustomerDriverMatched(ctx, rideID, c)

	e.log.Info().Str("ride_id", rideID).Str("driver_id", c.profileID).Msg("matching: driver accepted")
}

func (e *Engine) notifyCustomerDriverMatched(ctx context.Context, rideID string, c *candidate) {
	payload := map[string]interface{}{
		"driver_id":  c.profileID,
		"distance_m": c.distanceM,
	}
	if info, err := e.driverRepo.GetMatchNotificationInfo(ctx, c.profileID); err == nil && info != nil {
		payload["driver_name"] = info.FullName
		payload["driver_phone"] = info.Phone
		payload["vehicle_plate"] = info.VehiclePlate
		payload["transport_type"] = info.TransportType
		if info.Lat != 0 || info.Lng != 0 {
			payload["lat"] = info.Lat
			payload["lng"] = info.Lng
		}
	}
	e.hub.SendToCustomer(rideID, tracking.Message{
		Type:    "driver_matched",
		RideID:  rideID,
		Payload: payload,
	})
}

func (e *Engine) onDeclined(ctx context.Context, rideID string, c *candidate) {
	e.log.Info().Str("ride_id", rideID).Str("driver_id", c.profileID).Msg("matching: driver declined/timeout")
	key := rkeys.K.DriverDailyDeclines(c.profileID)
	e.redis.Incr(ctx, key)
	e.redis.ExpireAt(ctx, key, endOfDay())
}

func sortCandidates(cs []*candidate) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].score < cs[j-1].score; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

func endOfDay() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
}
