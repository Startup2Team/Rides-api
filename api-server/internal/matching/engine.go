package matching

import (
	"context"
	"fmt"
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
	"github.com/workspace/ride-platform/pkg/timeutil"
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
	redis      goredis.UniversalClient
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
	rdb goredis.UniversalClient,
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
//
// The context carries a hard deadline so a search cannot run unbounded. Without
// it the worst case was data-dependent (candidates × offer timeout) with nothing
// in code enforcing a ceiling, and the customer's screen had no timeout either.
func (e *Engine) StartSearch(rideID string, pickup geo.Point, transportType string) {
	giveUp := time.Duration(e.cfg.Matching.GiveUpSeconds) * time.Second
	if giveUp <= 0 {
		giveUp = 90 * time.Second
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), giveUp)
		defer cancel()
		e.runLoop(ctx, rideID, pickup, transportType)
	}()
}

// acceptSignal carries the responding driver's identity so the matching loop
// can verify it against the candidate currently being offered. Without this,
// the channel is keyed only by ride_id and any driver's accept would be applied
// to whichever candidate the loop happens to be offering (authZ + wrong-assign
// hole).
type acceptSignal struct {
	driverID string // driver_profiles.id of the responder ("" = unchecked, legacy)
	accepted bool
}

// NotifyAccept is called by the driver accept/decline handler. driverID is the
// responding driver's profile id; the matching loop ignores signals whose
// driverID doesn't match the driver currently being offered the ride.
func (e *Engine) NotifyAccept(rideID, driverID string, accepted bool) bool {
	if ch, ok := e.acceptChannels.Load(rideID); ok {
		select {
		case ch.(chan acceptSignal) <- acceptSignal{driverID: driverID, accepted: accepted}:
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

	// Radius expands each round: primary → expanded → 2× expanded → …
	// This way a ride in a quiet area keeps searching wider rather than
	// failing immediately after the first empty ring.
	baseRadius := e.cfg.Matching.PrimaryRadiusM

	waveInterval := time.Duration(e.cfg.Matching.WaveIntervalSeconds) * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// A round after the first waits before widening. Previously this loop did
		// a bare `continue`, so every attempt ran back-to-back in milliseconds:
		// with nobody online the "3 attempts" were three identical queries in the
		// same instant and the ride was cancelled before the customer's app had
		// even opened its websocket. The wait is the point — it gives drivers time
		// to come online or finish a trip.
		if attempt > 0 && waveInterval > 0 {
			select {
			case <-time.After(waveInterval):
			case <-ctx.Done():
				e.log.Info().Str("ride_id", rideID).Int("attempt", attempt).
					Msg("matching: deadline reached while waiting to widen")
				e.giveUp(context.WithoutCancel(ctx), rideID,
					"no driver found before deadline", "No driver found nearby. Please try again.")
				return
			}
		}

		// Stop if the ride is no longer searchable — the customer may have
		// cancelled, or another path may have resolved it. The loop used to run on
		// context.Background() and never re-read status, so offers kept going out
		// for cancelled rides and a driver could accept one, ending up stuck
		// ON_TRIP on a dead ride.
		if cur, err := e.rideRepo.FindByID(ctx, rideID); err == nil && cur != nil {
			if cur.Status != ride.StatusSearching {
				e.log.Info().Str("ride_id", rideID).Str("status", string(cur.Status)).
					Msg("matching: ride no longer SEARCHING — stopping")
				return
			}
		}

		// Double the search radius on each attempt after the first.
		currentRadius := baseRadius * (1 << attempt) // 1×, 2×, 4×, …
		if currentRadius > e.cfg.Matching.ExpandedRadiusM {
			currentRadius = e.cfg.Matching.ExpandedRadiusM
		}

		candidates, err := e.searchCandidatesWithRadius(ctx, pickup, transportType, tried, currentRadius)
		if err != nil {
			e.log.Warn().Err(err).Str("ride_id", rideID).Int("attempt", attempt).Msg("matching: candidate search error")
			break
		}
		if len(candidates) == 0 {
			e.log.Debug().Str("ride_id", rideID).Int("attempt", attempt).Int("radius_m", currentRadius).Msg("matching: no candidates at radius, expanding")
			continue
		}

		for _, c := range candidates {
			if tried[c.profileID] {
				continue
			}
			// Skip seeded/offline drivers with no live socket — otherwise each offer waits
			// for the full match timeout before trying the next candidate.
			if !e.hub.IsDriverConnected(c.profileID) {
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
	e.giveUp(ctx, rideID, "no driver found after max attempts", "No driver found nearby. Please try again.")
}

// giveUp ends an unsuccessful search and — critically — releases the Redis state
// the search reserved.
//
// The previous version cancelled the ride in Postgres and published a websocket
// message, and touched Redis not at all. Three things followed:
//
//  1. `customer:<id>:active_ride` is written with no TTL and was never cleared,
//     while CreateRide rejects on that key's presence alone without consulting
//     the ride's actual status. A customer who searched with no drivers online
//     became PERMANENTLY unable to book again — and "Cancel Search" did not fix
//     it either, because CancelRide early-returns on an already-terminal ride
//     before reaching its own cleanup. Recovery needed manual Redis surgery.
//
//  2. `ride:<id>:state` stayed "SEARCHING" for its 15-minute TTL. The socket's
//     state-replay reads exactly that key, so a reconnecting app was told to go
//     back to the searching screen for a ride the database had cancelled —
//     a deterministic infinite spinner.
//
//  3. The `ride_cancelled` websocket message was published before the customer's
//     socket existed (the app only connects after POST /customer/rides returns,
//     and matching could finish first), and websocket sends are fire-and-forget
//     Redis publishes with no buffering. The message was simply dropped.
//
// Order matters here: write the terminal state to Redis BEFORE publishing, so a
// client that reconnects instead of receiving the push still learns the truth.
// FCM goes out alongside the socket message for the same reason.
func (e *Engine) giveUp(ctx context.Context, rideID, dbReason, customerMessage string) {
	e.log.Warn().Str("ride_id", rideID).Str("reason", dbReason).Msg("matching: giving up — cancelling ride")

	r, findErr := e.rideRepo.FindByID(ctx, rideID)

	if _, err := e.rideRepo.Cancel(ctx, rideID, dbReason, "SYSTEM"); err != nil {
		e.log.Error().Err(err).Str("ride_id", rideID).Msg("matching: could not cancel ride on give-up")
	}
	_ = e.rideRepo.AppendEvent(ctx, rideID, "ride.cancelled", "SYSTEM", rideID, map[string]interface{}{
		"reason": "no_driver_found",
	})
	e.analytics.Publish(ctx, "ride.cancelled", "SYSTEM", rideID, &rideID, map[string]interface{}{
		"ride_id": rideID, "reason": "no_driver_found",
	})

	// Release everything the search reserved. Without this the customer cannot
	// book again, and the state replay contradicts the database.
	e.redis.Del(ctx, rkeys.K.RidePendingDriver(rideID))
	e.redis.Del(ctx, rkeys.K.RideExcludedDrivers(rideID))
	if findErr == nil && r != nil {
		e.redis.Del(ctx, rkeys.K.CustomerActiveRide(r.CustomerID))
		// Short-lived CANCELLED marker rather than a delete: the replay path
		// distinguishes "cancelled" from "unknown ride", and it self-expires.
		e.redis.Set(ctx, rkeys.K.RideState(rideID), string(ride.StatusCancelled), 2*time.Minute)
	} else {
		e.log.Error().Err(findErr).Str("ride_id", rideID).
			Msg("matching: could not load ride on give-up — customer active_ride pointer may be stale")
		e.redis.Del(ctx, rkeys.K.RideState(rideID))
	}

	e.hub.SendToCustomer(rideID, tracking.Message{
		Type: "ride_cancelled", RideID: rideID,
		Payload: map[string]interface{}{"reason": customerMessage},
	})
	// The socket may not have been connected when the message went out, so also
	// push. This is what driver_matched already does.
	if findErr == nil && r != nil {
		e.notify.SendToAllDevices(ctx, r.CustomerID, "No driver found",
			customerMessage, "ride",
			map[string]string{"type": "ride_cancelled", "ride_id": rideID})
	}
}

// searchCandidatesWithRadius uses Redis GEO to find nearby drivers within the given radius,
// enriches and scores them.
func (e *Engine) searchCandidatesWithRadius(ctx context.Context, pickup geo.Point, vehicleType string, tried map[string]bool, radiusM int) ([]*candidate, error) {
	geoKey := rkeys.K.DriverGeoIndex(vehicleType)

	results, err := e.redis.GeoSearchLocation(ctx, geoKey, &goredis.GeoSearchLocationQuery{
		GeoSearchQuery: goredis.GeoSearchQuery{
			Longitude:  pickup.Lng,
			Latitude:   pickup.Lat,
			Radius:     float64(radiusM) / 1000.0,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      10,
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()

	if err != nil || len(results) == 0 {
		return e.fallbackPostGIS(ctx, pickup, vehicleType, tried, radiusM)
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
		normalizedDist := distM / float64(radiusM)
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
func (e *Engine) fallbackPostGIS(ctx context.Context, pickup geo.Point, vehicleType string, tried map[string]bool, radiusM int) ([]*candidate, error) {
	var excludedIDs []string
	for id := range tried {
		excludedIDs = append(excludedIDs, id)
	}

	nearby, err := e.driverRepo.FindNearby(ctx, pickup, radiusM, vehicleType, excludedIDs)
	if err != nil {
		return nil, err
	}

	var candidates []*candidate
	for _, n := range nearby {
		declines := 0
		if d, err := e.redis.Get(ctx, rkeys.K.DriverDailyDeclines(n.ProfileID)).Int(); err == nil {
			declines = d
		}

		normalizedDist := n.DistanceM / float64(radiusM)
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

	// Persist an in-app notification AND push to every device the driver has
	// registered (best-effort, dead tokens pruned) so a backgrounded driver app
	// wakes for the offer — not only the live WebSocket path below.
	e.notify.SendToAllDevices(ctx, c.userID, "New ride request",
		fmt.Sprintf("A rider is %.0fm away. Tap to view the request.", c.distanceM),
		"ride", map[string]string{"type": "ride_request", "ride_id": rideID})

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
	e.hub.SendToDriver(c.profileID, tracking.Message{
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

	acceptCh := make(chan acceptSignal, 1)
	e.acceptChannels.Store(rideID, acceptCh)
	defer e.acceptChannels.Delete(rideID)

	timer := time.NewTimer(ttl)
	defer timer.Stop()

	for {
		select {
		case sig := <-acceptCh:
			// Only the driver currently being offered this ride may resolve it.
			// A stale signal from a previously-offered driver (or any other
			// driver probing the ride_id) is ignored so it can't hijack or
			// prematurely decline the current offer.
			if sig.driverID != "" && sig.driverID != c.profileID {
				continue
			}
			return sig.accepted, true
		case <-timer.C:
			e.redis.Del(ctx, rkeys.K.RidePendingDriver(rideID))
			return false, true
		}
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
	// Wake a backgrounded customer app: a driver accepted, fare negotiation is next.
	if r, err := e.rideRepo.FindByID(ctx, rideID); err == nil {
		e.notify.SendToAllDevices(ctx, r.CustomerID, "Driver found",
			"A driver accepted your ride. Agree on a fare to confirm.", "ride",
			map[string]string{"type": "driver_matched", "ride_id": rideID})
	}
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

// endOfDay is kept as a package-level alias for readability at call sites.
func endOfDay() time.Time { return timeutil.EndOfDay() }
