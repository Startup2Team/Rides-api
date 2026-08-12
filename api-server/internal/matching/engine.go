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

// ──────────────────────────────────────────────────────────────────────────
// Internal matching loop
// ──────────────────────────────────────────────────────────────────────────

func (e *Engine) runLoop(ctx context.Context, rideID string, pickup geo.Point, transportType string) {
	// Bands are derived from the ETA promise and THIS vehicle type's speed, so a
	// Fuso search widens over shorter distances than a moto search for the same
	// promised wait. Pure arithmetic on load-time values — no query.
	tiers := e.cfg.Matching.TierRadiiForVehicle(transportType)
	if len(tiers) == 0 {
		tiers = []int{e.cfg.Matching.PrimaryRadiusM, e.cfg.Matching.ExpandedRadiusM}
	}
	maxRadius := tiers[len(tiers)-1]
	e.log.Debug().Str("ride_id", rideID).Str("vehicle", transportType).
		Ints("tier_radii_m", tiers).Msg("matching: derived broadcast bands")

	batchSize := e.cfg.Matching.BatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	window := time.Duration(e.cfg.Matching.TierWindowSeconds) * time.Second
	if window <= 0 {
		// Match the config default (and the driver app's 15s offer countdown).
		window = 15 * time.Second
	}
	waveInterval := time.Duration(e.cfg.Matching.WaveIntervalSeconds) * time.Second

	tried := make(map[string]bool)
	// declined tracks EXPLICIT "no" answers, separately from offered-but-silent.
	// tried marks drivers at offer time, so before this split a driver whose
	// phone was in a pocket for one 15s window was excluded for the whole search
	// exactly like one who tapped Decline — and the loop then slept out the rest
	// of its budget with a reachable driver it refused to ask again.
	declined := make(map[string]bool)
	// One second-chance pass per search: enough to re-ask the silent ones once,
	// without turning the search into a nag loop against the same dead phones.
	secondChanceUsed := false

	for {
		if ctx.Err() != nil {
			e.giveUp(context.WithoutCancel(ctx), rideID,
				"no driver found before deadline", "No driver found nearby. Please try again.")
			return
		}

		// Stop if the ride is no longer searchable — the customer may have
		// cancelled, or another path resolved it. Without this the loop kept
		// offering a dead ride and an accepting driver ended up stuck ON_TRIP.
		if cur, err := e.rideRepo.FindByID(ctx, rideID); err == nil && cur != nil {
			if cur.Status != ride.StatusSearching {
				e.log.Info().Str("ride_id", rideID).Str("status", string(cur.Status)).
					Msg("matching: ride no longer SEARCHING — stopping")
				return
			}
		}

		// ONE query, at the WIDEST radius, sorted nearest-first.
		//
		// The old loop issued a fresh query per radius ring. That was wasted work:
		// GEOSEARCH already returns results sorted ascending by distance, so a
		// 3km query *contains* the 800m and 1500m results in order. Querying
		// narrow-then-wide re-fetched the same nearest drivers repeatedly — and
		// because Count capped results at 10, widening usually returned the very
		// same ten rows, which is why "expanding the radius" achieved so little.
		candidates, err := e.searchCandidatesWithRadius(ctx, pickup, transportType, tried, maxRadius)
		if err != nil {
			e.log.Warn().Err(err).Str("ride_id", rideID).Msg("matching: candidate search error")
		}

		offeredAnyone := false
		for _, tierRadius := range tiers {
			if ctx.Err() != nil {
				break
			}

			// Take the next batch from within this band. Candidates arrive already
			// ordered by score (distance dominating at 0.6), so this is
			// nearest-and-best first within the band.
			batch := make([]*candidate, 0, batchSize)
			for _, c := range candidates {
				if len(batch) >= batchSize {
					break
				}
				if tried[c.profileID] || c.distanceM > float64(tierRadius) {
					continue
				}
				if !e.hub.IsDriverConnected(c.profileID) {
					continue
				}
				batch = append(batch, c)
			}
			if len(batch) == 0 {
				continue
			}
			for _, c := range batch {
				tried[c.profileID] = true
			}
			offeredAnyone = true

			e.log.Info().Str("ride_id", rideID).
				Int("tier_radius_m", tierRadius).Int("batch", len(batch)).
				Msg("matching: broadcasting offer to batch")

			if winner := e.offerToBatch(ctx, rideID, batch, window, declined); winner != nil {
				e.onAccepted(ctx, rideID, winner)
				return
			}
		}

		if !offeredAnyone {
			// Pool exhausted. Before sleeping out the budget, give the silent
			// ones one more chance: clear offered-but-silent drivers from `tried`
			// (explicit decliners stay excluded) and re-offer immediately —
			// provided at least one full window of budget remains to hear back.
			if !secondChanceUsed {
				if dl, ok := ctx.Deadline(); ok && time.Until(dl) >= window {
					if cleared := releaseSilentTried(tried, declined); cleared > 0 {
						secondChanceUsed = true
						e.log.Info().Str("ride_id", rideID).Int("cleared", cleared).
							Msg("matching: pool exhausted — re-offering to silent drivers")
						continue
					}
				}
			}

			// Nobody reachable at any distance. Waiting is the only lever left:
			// a driver who comes online or finishes a trip in the next few seconds
			// is invisible to a search that gives up immediately. The old loop did
			// a bare `continue` here with no sleep, so all its "attempts" burned
			// out in the same millisecond.
			select {
			case <-time.After(waveInterval):
			case <-ctx.Done():
				e.giveUp(context.WithoutCancel(ctx), rideID,
					"no driver found before deadline", "No driver found nearby. Please try again.")
				return
			}
		}
	}
}

// releaseSilentTried removes offered-but-silent drivers from the tried set so
// one more offering pass can reach them; explicit decliners stay excluded — a
// spoken "no" is an answer, a missed window may just be a pocketed phone.
// Returns how many drivers were freed for re-offer.
func releaseSilentTried(tried, declined map[string]bool) int {
	cleared := 0
	for id := range tried {
		if !declined[id] {
			delete(tried, id)
			cleared++
		}
	}
	return cleared
}

// offerToBatch broadcasts one ride to several nearby drivers at once and returns
// whoever accepts first, or nil if the window closes with nobody.
//
// Offers used to be strictly sequential, each blocking for the full 15s match
// timeout before the next driver was tried. Four drivers who simply had their
// phones in a pocket cost a full minute of a passenger's time, and no amount of
// radius tuning touched that — the wait was in the offer loop, not the search.
// Broadcasting collapses time-to-match to roughly one window regardless of how
// many drivers ignore it, which is how Uber and Bolt behave.
//
// Correctness under a race: several drivers can accept in the same instant, so the
// winner is decided by a single SET NX on RideClaimedBy. Exactly one succeeds; the
// rest are told the ride is taken. Without that atomic step two drivers could both
// be assigned, or one could be assigned while the other's app believed it had won.
// declined is the search-wide map of explicit "no" answers; offerToBatch marks
// this batch's decliners in it so the second-chance pass can tell them apart
// from drivers who simply never answered.
func (e *Engine) offerToBatch(ctx context.Context, rideID string, batch []*candidate, window time.Duration, declined map[string]bool) *candidate {
	if len(batch) == 0 {
		return nil
	}

	// Reserve each driver so a concurrent search for a different ride cannot offer
	// to them at the same time. Drivers already locked elsewhere are dropped.
	offered := make([]*candidate, 0, len(batch))
	for _, c := range batch {
		ok, err := e.redis.SetNX(ctx, rkeys.K.MatchingLock(c.profileID), rideID, matchLockTTL).Result()
		if err == nil && ok {
			offered = append(offered, c)
		}
	}
	if len(offered) == 0 {
		return nil
	}
	defer func() {
		for _, c := range offered {
			e.redis.Del(ctx, rkeys.K.MatchingLock(c.profileID))
		}
		e.redis.Del(ctx, rkeys.K.RidePendingDrivers(rideID))
	}()

	// The accept endpoint authorises against this set, so it must be written
	// BEFORE any offer goes out — otherwise a very fast driver's accept arrives
	// while the set is still empty and is rejected as "not your offer".
	ids := make([]interface{}, 0, len(offered))
	for _, c := range offered {
		ids = append(ids, c.profileID)
	}
	e.redis.SAdd(ctx, rkeys.K.RidePendingDrivers(rideID), ids...)
	e.redis.Expire(ctx, rkeys.K.RidePendingDrivers(rideID), window+10*time.Second)

	acceptCh := make(chan acceptSignal, len(offered))
	e.acceptChannels.Store(rideID, acceptCh)
	defer e.acceptChannels.Delete(rideID)

	inBatch := make(map[string]*candidate, len(offered))
	for _, c := range offered {
		inBatch[c.profileID] = c
		e.sendOffer(ctx, rideID, c, window)
	}

	timer := time.NewTimer(window)
	defer timer.Stop()

	// The ride can stop being offerable while we wait (customer cancelled the
	// search, recovery resolved it). A cheap Redis poll on the state mirror ends
	// the window early instead of holding drivers on a dead offer.
	statusTick := time.NewTicker(2 * time.Second)
	defer statusTick.Stop()

	// responded tracks who answered this window at all — decliners and the
	// winner — so the timeout path can charge the silent remainder.
	responded := make(map[string]bool, len(offered))
	declines := 0
	for {
		select {
		case sig := <-acceptCh:
			c, ours := inBatch[sig.driverID]
			if !ours {
				// A driver outside this batch probing the ride_id, or a stale
				// signal from an earlier round. Ignore rather than let it
				// resolve someone else's offer.
				continue
			}
			if !sig.accepted {
				e.onDeclined(ctx, rideID, c)
				responded[c.profileID] = true
				declined[c.profileID] = true
				declines++
				if declines >= len(offered) {
					// Everyone said no; don't burn the rest of the window.
					return nil
				}
				continue
			}
			// Atomic winner selection.
			won, err := e.redis.SetNX(ctx, rkeys.K.RideClaimedBy(rideID), c.profileID, window+time.Minute).Result()
			if err != nil || !won {
				e.hub.SendToDriver(c.profileID, tracking.Message{
					Type: "ride_taken", RideID: rideID,
					Payload: map[string]interface{}{"reason": "Another driver accepted first."},
				})
				continue
			}
			// Record the winner where the rest of the system expects a single value.
			e.redis.Set(ctx, rkeys.K.RidePendingDriver(rideID), c.profileID, window+time.Minute)
			for _, other := range offered {
				if other.profileID == c.profileID {
					continue
				}
				e.hub.SendToDriver(other.profileID, tracking.Message{
					Type: "ride_taken", RideID: rideID,
					Payload: map[string]interface{}{"reason": "Another driver accepted first."},
				})
			}
			return c
		case <-timer.C:
			// A silent miss costs the same daily-decline strike as an explicit
			// "no". Only the explicit path was counted before, so the dominant
			// way drivers actually duck offers — just not answering — was free,
			// and cherry-pickers outscored honest decliners.
			for _, c := range offered {
				if !responded[c.profileID] {
					e.onDeclined(ctx, rideID, c)
				}
			}
			return nil
		case <-statusTick.C:
			if state, serr := e.redis.Get(ctx, rkeys.K.RideState(rideID)).Result(); serr == nil && state != string(ride.StatusSearching) {
				// No decline strikes here: the offer died on them, not the
				// other way round. CancelRide dismisses their offer cards.
				return nil
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// IsOfferedTo reports whether the ride is currently offered to this driver.
//
// Replaces a single-value check: with batched offers, "who may accept" is a set,
// and comparing against one stored ID would have rejected two of every three
// drivers in a batch with NOT_YOUR_OFFER.
func (e *Engine) IsOfferedTo(ctx context.Context, rideID, driverProfileID string) bool {
	ok, err := e.redis.SIsMember(ctx, rkeys.K.RidePendingDrivers(rideID), driverProfileID).Result()
	if err == nil && ok {
		return true
	}
	// Fall back to the single-driver key so an offer made by an older build (or
	// the winner path) still validates.
	id, err := e.redis.Get(ctx, rkeys.K.RidePendingDriver(rideID)).Result()
	return err == nil && id == driverProfileID
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
	// Fixed reference for the whole search. It used to divide by whichever ring the
	// current round used, so the same driver scored 0.95 found by a 2km ring and
	// 0.19 by a 10km one — the distance weight meant nothing across rounds. There
	// is now ONE query per search at the widest band, so radiusM is constant here,
	// and it scales per vehicle automatically: a Fuso search normalises over its
	// own shorter bands rather than a moto's.
	scoreRef := float64(radiusM)
	if scoreRef <= 0 {
		scoreRef = 1
	}

	geoKey := rkeys.K.DriverGeoIndex(vehicleType)

	results, err := e.redis.GeoSearchLocation(ctx, geoKey, &goredis.GeoSearchLocationQuery{
		GeoSearchQuery: goredis.GeoSearchQuery{
			Longitude:  pickup.Lng,
			Latitude:   pickup.Lat,
			Radius:     float64(radiusM) / 1000.0,
			RadiusUnit: "km",
			Sort:       "ASC",
			// Raised from 10. With tiered batching the loop needs enough of the
			// sorted list to fill several bands; capping at 10 meant widening the
			// radius usually returned the very same ten drivers, which is why
			// expansion accomplished so little.
			Count: 30,
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

		// Location freshness. Redis GEO members never expire, so a driver who
		// closed the app and went home stays in the index indefinitely and would
		// keep matching against their last known position. driver:<id>:location is
		// written with a 120s TTL on every fix, so its mere existence is a
		// free freshness signal — no extra bookkeeping needed. Until now the
		// socket check happened to mask this; it would have surfaced the moment
		// that check was relaxed.
		if n, err := e.redis.Exists(ctx, rkeys.K.DriverLocation(profileID)).Result(); err != nil || n == 0 {
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
		normalizedDist := math.Min(distM, scoreRef) / scoreRef
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
	// Fixed reference for the whole search. It used to divide by whichever ring the
	// current round used, so the same driver scored 0.95 found by a 2km ring and
	// 0.19 by a 10km one — the distance weight meant nothing across rounds. There
	// is now ONE query per search at the widest band, so radiusM is constant here,
	// and it scales per vehicle automatically: a Fuso search normalises over its
	// own shorter bands rather than a moto's.
	scoreRef := float64(radiusM)
	if scoreRef <= 0 {
		scoreRef = 1
	}

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

		normalizedDist := math.Min(n.DistanceM, scoreRef) / scoreRef
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
// sendOffer dispatches one ride offer to one driver and returns immediately.
//
// Split out of the old offerToDriver, which sent AND then blocked for the full
// match timeout waiting for that driver alone. offerToBatch needs the send
// without the wait so it can put the same ride in front of several drivers at
// once and let them race.
//
// Both transports are used deliberately: the WebSocket reaches a driver with the
// app open, and the push wakes one whose app is backgrounded. A driver holding a
// live socket is still worth pushing to, because "app in foreground" and "phone in
// pocket" are not the same thing.
func (e *Engine) sendOffer(ctx context.Context, rideID string, c *candidate, window time.Duration) {
	e.notify.SendToAllDevices(ctx, c.userID, "New ride request",
		fmt.Sprintf("A rider is %.0fm away. Tap to view the request.", c.distanceM),
		"ride", map[string]string{"type": "ride_request", "ride_id": rideID})

	payload := map[string]interface{}{
		"ride_id":    rideID,
		"distance_m": c.distanceM,
		// The server's real answer window, so the app can count down the truth
		// instead of hardcoding its own guess.
		"window_seconds": int(window.Seconds()),
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
}

func (e *Engine) onAccepted(ctx context.Context, rideID string, c *candidate) {
	// These writes were fire-and-forget, which is how an accept that raced a
	// customer cancel marched the driver into ON_TRIP on a dead ride: the DB
	// rejected the transitions, the Redis writes below happened anyway, and the
	// driver was stuck until something else reset them. The driver's Redis state
	// is only touched after ALL DB steps succeed, so any failure leaves them
	// AVAILABLE and still in the geo index — nothing to roll back.
	rejected := func(step string, err error) {
		e.log.Warn().Err(err).Str("ride_id", rideID).Str("driver_id", c.profileID).Str("step", step).
			Msg("matching: accept lost the race with a ride resolution — driver stays AVAILABLE")
		e.hub.SendToDriver(c.profileID, tracking.Message{
			Type: "ride_taken", RideID: rideID,
			Payload: map[string]interface{}{"reason": "This ride is no longer available."},
		})
	}
	// AssignDriver is guarded on status = SEARCHING, so a cancelled ride refuses
	// the driver here rather than recording one.
	if err := e.rideRepo.AssignDriver(ctx, rideID, c.profileID); err != nil {
		rejected("assign", err)
		return
	}
	if err := e.rideRepo.Transition(ctx, rideID, ride.StatusSearching, ride.StatusMatched); err != nil {
		// Assigned, then the ride resolved under us. Clear the assignment so the
		// cancelled ride doesn't name a driver who never took it.
		_ = e.rideRepo.UnassignDriver(ctx, rideID)
		rejected("searching→matched", err)
		return
	}
	if err := e.rideRepo.Transition(ctx, rideID, ride.StatusMatched, ride.StatusNegotiating); err != nil {
		_ = e.rideRepo.UnassignDriver(ctx, rideID)
		rejected("matched→negotiating", err)
		return
	}

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
	e.redis.ExpireAt(ctx, key, e.endOfDay())
}

func sortCandidates(cs []*candidate) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].score < cs[j-1].score; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// endOfDay is when today's decline counter stops counting — local midnight in
// the platform timezone, matching the driver and ride services.
func (e *Engine) endOfDay() time.Time {
	return timeutil.EndOfLocalDay(time.Now(), e.cfg.Location())
}
