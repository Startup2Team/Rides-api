package matching

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// batchSolveTimeout bounds the whole pre-pass (candidate gather + one OSRM
// /table call + the solve). It is deliberately larger than the routing client's
// own 2s HTTP timeout so a slow-but-succeeding OSRM call isn't cut off, yet
// still guarantees a flush can't hang forever. On timeout the batch fails
// closed to per-ride greedy search.
const batchSolveTimeout = 3 * time.Second

// pendingRide is a ride waiting in a batching window. Purely in-memory and
// NON-AUTHORITATIVE: the ride is already SEARCHING in Postgres + Redis (written
// by CreateRide) before it is ever enqueued, so a crash mid-window loses only
// the batching optimisation — the recovery job re-queues the ride and it
// degrades to greedy for one search. No source-of-truth state lives here.
type pendingRide struct {
	rideID      string
	pickup      geo.Point
	vehicleType string
	enqueuedAt  time.Time
}

// dispatchBatcher collects concurrent SEARCHING rides into short per-bucket
// windows and hands each window to Engine.dispatchBatch for a global min-cost
// assignment. It is a pure REORDERING layer in front of the executor: it never
// touches Postgres, never writes pending_drivers / claimed_by / matching:lock,
// and never assigns a driver — it only decides which driver each ride is
// OFFERED FIRST.
type dispatchBatcher struct {
	engine *Engine
	window time.Duration
	cellM  float64

	mu      sync.Mutex
	buckets map[string][]pendingRide
	timers  map[string]*time.Timer
}

// newDispatchBatcher builds a batcher from the engine's config. Only constructed
// (in NewEngine) when MATCH_BATCHED_DISPATCH is true, so a nil batcher is the
// off switch and the StartSearch path stays byte-for-byte today's behavior.
func newDispatchBatcher(e *Engine) *dispatchBatcher {
	window := time.Duration(e.cfg.Matching.BatchWindowMs) * time.Millisecond
	if window <= 0 {
		window = 2 * time.Second
	}
	cellM := float64(e.cfg.Matching.BatchGeoCellM)
	if cellM <= 0 {
		cellM = 5000
	}
	return &dispatchBatcher{
		engine:  e,
		window:  window,
		cellM:   cellM,
		buckets: make(map[string][]pendingRide),
		timers:  make(map[string]*time.Timer),
	}
}

// bucketKey groups rides that can meaningfully be co-assigned: same vehicle type
// (a moto can't serve a Fuso request — the geo index is per vehicle) and the
// same coarse square grid cell (two pickups far apart share no useful drivers
// and only inflate the OSRM matrix). The grid is intentionally crude: bucketing
// only needs to be coarse and stable, not geodesically exact.
func (b *dispatchBatcher) bucketKey(vehicleType string, p geo.Point) string {
	latDeg := b.cellM / 111000.0 // ~metres per degree latitude
	if latDeg <= 0 {
		latDeg = 0.045
	}
	lngDeg := latDeg * math.Cos(p.Lat*math.Pi/180.0)
	if lngDeg <= 0 {
		lngDeg = latDeg
	}
	latCell := int(math.Floor(p.Lat / latDeg))
	lngCell := int(math.Floor(p.Lng / lngDeg))
	return fmt.Sprintf("%s:%d:%d", vehicleType, latCell, lngCell)
}

// enqueue adds a ride to its bucket and arms the bucket's flush timer if it is
// the first ride in the window. Cheap and non-blocking — the flush happens later
// on its own goroutine.
func (b *dispatchBatcher) enqueue(pr pendingRide) {
	key := b.bucketKey(pr.vehicleType, pr.pickup)
	b.mu.Lock()
	b.buckets[key] = append(b.buckets[key], pr)
	if _, armed := b.timers[key]; !armed {
		b.timers[key] = time.AfterFunc(b.window, func() { b.flush(key) })
	}
	b.mu.Unlock()
}

// flush drains a bucket and runs its assignment. It takes the bucket contents
// under the mutex, then releases it BEFORE any solve/OSRM/Redis work, so the
// batcher lock is never held across a network call.
func (b *dispatchBatcher) flush(key string) {
	b.mu.Lock()
	rides := b.buckets[key]
	delete(b.buckets, key)
	delete(b.timers, key)
	b.mu.Unlock()

	if len(rides) == 0 {
		return
	}
	b.engine.dispatchBatch(rides)
}

// dispatchBatch runs the Phase-B assignment for one bucket flush, entirely OFF
// the lock path: it holds no batcher or engine mutex, does at most one OSRM
// /table call plus a bounded number of Redis reads, and NEVER writes
// pending_drivers, claimed_by, or matching:lock — those stay exclusively in the
// executor (offerToBatch / onAccepted). It only decides which driver each ride
// is offered FIRST, then hands every ride to the normal runLoop (seeded or not).
//
// Fail-closed at every step: a single-ride window, a solve error, or no
// candidates all fall back to today's greedy per-ride search. No ride is ever
// dropped, and a seeded driver that declines/times out self-heals via the
// normal banded broadcast.
func (e *Engine) dispatchBatch(rides []pendingRide) {
	// One ride in the window gains nothing from a global solve — take the
	// existing path with zero added OSRM/Hungarian cost.
	if len(rides) == 1 {
		e.launchSearch(rides[0], nil)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), batchSolveTimeout)
	defer cancel()

	seeds, err := e.solveBatch(ctx, rides)
	if err != nil || len(seeds) != len(rides) {
		e.log.Warn().Err(err).Int("rides", len(rides)).
			Msg("matching: batch solve failed — falling back to per-ride greedy search")
		for _, r := range rides {
			e.launchSearch(r, nil)
		}
		return
	}

	assigned := 0
	for i, r := range rides {
		if seeds[i] != nil {
			assigned++
		}
		// seeds[i] == nil → this ride starts today's normal broadcast.
		e.launchSearch(r, seeds[i])
	}
	e.log.Info().Int("rides", len(rides)).Int("seeded", assigned).
		Msg("matching: batched dispatch flushed")
}

// solveBatch gathers candidate drivers for the batch, builds the driver×ride ETA
// cost matrix (OSRM with Haversine fallback), and returns one seed candidate per
// ride (nil where a ride was left unassigned by the fairness cap or has no
// candidate). Returns an error only for a genuine failure the caller should turn
// into a full greedy fallback; "no candidates" is a normal nil-seed result, not
// an error.
func (e *Engine) solveBatch(ctx context.Context, rides []pendingRide) ([]*candidate, error) {
	n := len(rides)
	seeds := make([]*candidate, n)

	k := e.cfg.Matching.BatchKPerRide
	if k < 1 {
		k = 8
	}
	maxDrivers := e.cfg.Matching.BatchMaxDrivers
	if maxDrivers < 1 {
		maxDrivers = 30
	}

	// 1. Gather each ride's k-nearest available drivers and union them, keeping a
	//    stable order and one representative candidate object per driver.
	union := make(map[string]*candidate)
	order := make([]string, 0, maxDrivers)
	for _, r := range rides {
		cands, err := e.searchCandidatesWithRadius(ctx, r.pickup, r.vehicleType, map[string]bool{}, e.batchMaxRadius(r.vehicleType))
		if err != nil {
			// This ride contributes no candidates; it may end up unassigned and
			// broadcast normally. Not a batch-wide failure.
			continue
		}
		added := 0
		for _, c := range cands {
			if added >= k || len(union) >= maxDrivers {
				break
			}
			if _, seen := union[c.profileID]; !seen {
				union[c.profileID] = c
				order = append(order, c.profileID)
			}
			added++
		}
	}
	if len(order) == 0 {
		return seeds, nil // nobody available anywhere → all normal broadcast
	}

	// 2. Exclude drivers holding a live matching:lock — an in-flight offer for
	//    another ride. This is the SAME guard the executor uses, so a driver
	//    seeded/offered in an earlier window is invisible here and can't be
	//    double-assigned. (The executor's SetNX remains the ground-truth guard;
	//    this only avoids seeding a doomed offer.)
	avail := make([]string, 0, len(order))
	for _, id := range order {
		if cnt, err := e.redis.Exists(ctx, rkeys.K.MatchingLock(id)).Result(); err == nil && cnt > 0 {
			continue
		}
		avail = append(avail, id)
	}
	if len(avail) == 0 {
		return seeds, nil
	}

	// 3. Cost matrix: rows = drivers, cols = ride pickups, value = ETA seconds.
	driverPts := make([]geo.Point, len(avail))
	for i, id := range avail {
		c := union[id]
		driverPts[i] = geo.Point{Lat: c.lat, Lng: c.lng}
	}
	pickups := make([]geo.Point, n)
	for j, r := range rides {
		pickups[j] = r.pickup
	}
	cost := e.batchCostMatrix(ctx, rides[0].vehicleType, driverPts, pickups)

	// 4. Fairness-capped min-cost assignment (pure, unit-tested core).
	colToRow := solveAssignment(cost, e.cfg.Matching.BatchETACapFactor, e.cfg.Matching.BatchETACapSlackS)

	// 5. Build one seed candidate per assigned ride. distanceM is recomputed
	//    against THIS ride's pickup (the representative's distanceM was measured
	//    against whichever ride first found the driver) — it feeds only the offer
	//    text / customer payload, never money or the band gate.
	for j := 0; j < n && j < len(colToRow); j++ {
		row := colToRow[j]
		if row < 0 || row >= len(avail) {
			continue
		}
		rep := union[avail[row]]
		seed := *rep
		seed.distanceM = geo.DistanceKM(geo.Point{Lat: rep.lat, Lng: rep.lng}, rides[j].pickup) * 1000
		seeds[j] = &seed
	}
	return seeds, nil
}

// batchCostMatrix returns a drivers×pickups ETA matrix in seconds. It prefers a
// single real-road OSRM /table call and falls back — fail-closed — to a
// Haversine × detour ÷ speed estimate on any OSRM error, disablement, or
// shape mismatch. The SAME global assignment then runs on either matrix, so the
// greedy-collision fix survives even when OSRM is off; OSRM only makes the ETAs
// more accurate. A nil OSRM cell (no route) becomes +Inf (forbidden pairing).
func (e *Engine) batchCostMatrix(ctx context.Context, vehicleType string, drivers, pickups []geo.Point) [][]float64 {
	rows, cols := len(drivers), len(pickups)
	cost := make([][]float64, rows)
	for i := range cost {
		cost[i] = make([]float64, cols)
	}

	if e.router != nil && e.router.Enabled() {
		res, err := e.router.Table(ctx, drivers, pickups)
		switch {
		case err != nil:
			e.log.Warn().Err(err).Msg("matching: batch OSRM table failed — using Haversine cost matrix")
		case len(res.DurationsSec) != rows:
			e.log.Warn().Int("rows", rows).Int("got", len(res.DurationsSec)).
				Msg("matching: batch OSRM table row mismatch — using Haversine cost matrix")
		default:
			shapeOK := true
			for i := 0; i < rows; i++ {
				if len(res.DurationsSec[i]) != cols {
					shapeOK = false
					break
				}
			}
			if shapeOK {
				for i := 0; i < rows; i++ {
					for j := 0; j < cols; j++ {
						if v := res.DurationsSec[i][j]; v != nil {
							cost[i][j] = *v
						} else {
							cost[i][j] = math.Inf(1)
						}
					}
				}
				return cost
			}
			e.log.Warn().Msg("matching: batch OSRM table column mismatch — using Haversine cost matrix")
		}
	}

	// Haversine fallback (same numbers the band math uses).
	speed := e.cfg.Matching.VehicleSpeedKmh[vehicleType]
	if speed <= 0 {
		speed = 14
	}
	detour := e.cfg.Matching.RoadDetourFactor
	if detour < 1 {
		detour = 1.4
	}
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			roadKM := geo.DistanceKM(drivers[i], pickups[j]) * detour
			cost[i][j] = roadKM / speed * 3600.0
		}
	}
	return cost
}

// batchMaxRadius is the widest broadcast band for a vehicle type — the same
// radius a single-ride search would ultimately query, so batching considers the
// same driver pool the greedy path would.
func (e *Engine) batchMaxRadius(vehicleType string) int {
	tiers := e.cfg.Matching.TierRadiiForVehicle(vehicleType)
	if len(tiers) == 0 {
		return e.cfg.Matching.ExpandedRadiusM
	}
	return tiers[len(tiers)-1]
}
