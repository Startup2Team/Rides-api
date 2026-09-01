package matching

import (
	"context"
	"math"
	"testing"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/pkg/geo"
)

func testEngine() *Engine {
	cfg := &config.Config{}
	cfg.Matching.BatchGeoCellM = 5000
	cfg.Matching.RoadDetourFactor = 1.4
	cfg.Matching.VehicleSpeedKmh = map[string]float64{"MOTO_BIKE": 22}
	cfg.Matching.BatchETACapFactor = 2.0
	cfg.Matching.BatchETACapSlackS = 120
	return &Engine{cfg: cfg, log: zerolog.Nop()}
}

func TestBucketKey_SameCellSameVehicleGroups(t *testing.T) {
	b := newDispatchBatcher(testEngine())
	// Two Kigali pickups ~500m apart — same 5km cell, same vehicle → same bucket.
	a := b.bucketKey("MOTO_BIKE", geo.Point{Lat: -1.9441, Lng: 30.0619})
	c := b.bucketKey("MOTO_BIKE", geo.Point{Lat: -1.9460, Lng: 30.0640})
	if a != c {
		t.Fatalf("nearby pickups should share a bucket: %q vs %q", a, c)
	}
}

func TestBucketKey_DifferentVehicleSeparates(t *testing.T) {
	b := newDispatchBatcher(testEngine())
	p := geo.Point{Lat: -1.9441, Lng: 30.0619}
	if b.bucketKey("MOTO_BIKE", p) == b.bucketKey("HEAVY_FUSO", p) {
		t.Fatal("different vehicle types must not share a bucket")
	}
}

func TestBucketKey_FarApartSeparates(t *testing.T) {
	b := newDispatchBatcher(testEngine())
	kigali := b.bucketKey("MOTO_BIKE", geo.Point{Lat: -1.9441, Lng: 30.0619})
	// ~40km east — different cell.
	far := b.bucketKey("MOTO_BIKE", geo.Point{Lat: -1.9441, Lng: 30.42})
	if kigali == far {
		t.Fatalf("far-apart pickups must not share a bucket: %q", kigali)
	}
}

func TestBatchCostMatrix_HaversineFallback(t *testing.T) {
	e := testEngine() // router nil → Haversine path
	drivers := []geo.Point{
		{Lat: -1.9441, Lng: 30.0619},
		{Lat: -1.9500, Lng: 30.0700},
	}
	pickups := []geo.Point{
		{Lat: -1.9441, Lng: 30.0619}, // co-located with driver 0
	}
	cost := e.batchCostMatrix(context.Background(), "MOTO_BIKE", drivers, pickups)
	if len(cost) != 2 || len(cost[0]) != 1 {
		t.Fatalf("want 2×1 matrix, got %dx%d", len(cost), len(cost[0]))
	}
	// Driver 0 is co-located → ~0 ETA; driver 1 is farther → strictly larger.
	if cost[0][0] > 1 {
		t.Fatalf("co-located driver should have ~0 ETA, got %.3f", cost[0][0])
	}
	if cost[1][0] <= cost[0][0] {
		t.Fatalf("farther driver should have larger ETA: d0=%.3f d1=%.3f", cost[0][0], cost[1][0])
	}
	// Sanity: ETA seconds ≈ haversine*detour/speed*3600 for driver 1.
	roadKM := geo.DistanceKM(drivers[1], pickups[0]) * 1.4
	want := roadKM / 22 * 3600
	if math.Abs(cost[1][0]-want) > 1e-6 {
		t.Fatalf("driver1 ETA=%.3f want %.3f", cost[1][0], want)
	}
}

func TestBatchMaxRadius_UsesWidestBand(t *testing.T) {
	e := testEngine()
	e.cfg.Matching.TierETAMinutes = []int{3, 6, 10}
	r := e.batchMaxRadius("MOTO_BIKE")
	if r <= 0 {
		t.Fatalf("want positive max radius, got %d", r)
	}
}
