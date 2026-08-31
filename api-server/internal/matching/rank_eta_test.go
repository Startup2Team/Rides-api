package matching

// Internal test package (not matching_test): rankByRoutedETA and the
// candidate/Engine fields it touches are unexported, and these tests need to
// construct an Engine directly and assert on candidate.score/order after the
// call — the same reason engine.go's unexported scoring formula is
// re-derived by hand in engine_test.go's external-package tests, except here
// we exercise the real method against a mock OSRM server instead of
// reimplementing it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/routing"
	"github.com/workspace/ride-platform/pkg/geo"
)

// newTestEngine builds a minimal Engine with just what rankByRoutedETA reads:
// cfg (for UseRoutedETA), log, and router. No Redis/Postgres deps needed —
// rankByRoutedETA never touches them.
func newTestEngine(t *testing.T, osrmURL string, useRoutedETA bool) *Engine {
	t.Helper()
	cfg := &config.Config{}
	cfg.Routing.OSRMURL = osrmURL
	cfg.Matching.UseRoutedETA = useRoutedETA
	return &Engine{
		cfg:    cfg,
		log:    zerolog.Nop(),
		router: routing.New(cfg, zerolog.Nop()),
	}
}

// straightLineCandidates returns two candidates already in straight-line
// score order (A nearer, B farther) — mirrors what searchCandidatesWithRadius
// hands rankByRoutedETA on entry. dailyDeclines=0 and acceptanceRate=100 on
// both so score collapses to the 0.6-weighted distance/ETA term alone,
// keeping the before/after arithmetic legible.
func straightLineCandidates() []*candidate {
	return []*candidate{
		{
			profileID: "driver-A", distanceM: 500, lat: -1.9441, lng: 30.0619,
			dailyDeclines: 0, acceptanceRate: 100, score: 0.05,
		},
		{
			profileID: "driver-B", distanceM: 1000, lat: -1.9500, lng: 30.0700,
			dailyDeclines: 0, acceptanceRate: 100, score: 0.10,
		},
	}
}

func tablePayload(t *testing.T, durations string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/table/v1/driving/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"Ok","durations":` + durations + `}`))
	}
}

// ── Real ETA reorders vs straight-line ──────────────────────────────────────

func TestRankByRoutedETA_ReordersDriverCloserByRoad(t *testing.T) {
	// driver-A is nearer by straight line (500m) but its road ETA (900s) is
	// worse than driver-B's (straight-line 1000m, but only 120s by road — the
	// "300m across an uncrossable river" case from the design doc). Real-ETA
	// ranking must put B first.
	srv := httptest.NewServer(tablePayload(t, `[[900.0],[120.0]]`))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true)
	require.True(t, e.router.Enabled())

	candidates := straightLineCandidates()
	require.Equal(t, "driver-A", candidates[0].profileID, "precondition: A ranks first by straight-line")

	e.rankByRoutedETA(context.Background(), "ride-1", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)

	require.Len(t, candidates, 2)
	assert.Equal(t, "driver-B", candidates[0].profileID, "closer-by-road driver must rank first after real-ETA ranking")
	assert.Equal(t, "driver-A", candidates[1].profileID)
	assert.Less(t, candidates[0].score, candidates[1].score, "winner must have the lower (better) score")
}

// ── OSRM disabled → straight-line order/scores untouched ───────────────────

func TestRankByRoutedETA_OSRMDisabled_FallsBackToStraightLineOrder(t *testing.T) {
	e := newTestEngine(t, "", true) // OSRM_URL unset — router.Enabled() == false
	require.False(t, e.router.Enabled())

	candidates := straightLineCandidates()
	wantOrder := []string{candidates[0].profileID, candidates[1].profileID}
	wantScores := []float64{candidates[0].score, candidates[1].score}

	e.rankByRoutedETA(context.Background(), "ride-2", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)

	assert.Equal(t, wantOrder, []string{candidates[0].profileID, candidates[1].profileID},
		"disabled OSRM must leave candidate order byte-for-byte unchanged")
	assert.Equal(t, wantScores, []float64{candidates[0].score, candidates[1].score},
		"disabled OSRM must leave scores untouched")
}

// ── UseRoutedETA=false → straight-line order/scores untouched even with a
// live, working OSRM (the two gates are independent) ────────────────────────

func TestRankByRoutedETA_FlagOff_FallsBackToStraightLineOrder(t *testing.T) {
	// This server would reorder candidates if consulted — proves the flag,
	// not just OSRM reachability, gates the behavior.
	srv := httptest.NewServer(tablePayload(t, `[[900.0],[120.0]]`))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, false) // MATCH_USE_ROUTED_ETA=false
	require.True(t, e.router.Enabled(), "OSRM itself is configured and reachable")

	candidates := straightLineCandidates()
	wantOrder := []string{candidates[0].profileID, candidates[1].profileID}

	e.rankByRoutedETA(context.Background(), "ride-3", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)

	assert.Equal(t, wantOrder, []string{candidates[0].profileID, candidates[1].profileID},
		"MATCH_USE_ROUTED_ETA=false must skip ranking even though OSRM is enabled and reachable")
}

// ── OSRM error (5xx) → falls back silently, order/scores untouched ─────────

func TestRankByRoutedETA_OSRMError_FallsBackToStraightLineOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"InternalError","message":"boom"}`))
	}))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true)
	candidates := straightLineCandidates()
	wantOrder := []string{candidates[0].profileID, candidates[1].profileID}
	wantScores := []float64{candidates[0].score, candidates[1].score}

	e.rankByRoutedETA(context.Background(), "ride-4", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)

	assert.Equal(t, wantOrder, []string{candidates[0].profileID, candidates[1].profileID},
		"an OSRM 500 must not stall or crash ranking — falls back to straight-line order")
	assert.Equal(t, wantScores, []float64{candidates[0].score, candidates[1].score})
}

// ── nil ETA cell for one candidate → that candidate keeps its straight-line
// score (excluded from re-ranking, not dropped or crashed) ─────────────────

func TestRankByRoutedETA_NilCell_KeepsStraightLineScoreForThatCandidate(t *testing.T) {
	// driver-A has no routable path (nil); driver-B does. A must keep its
	// original straight-line score untouched; B gets re-scored by ETA.
	srv := httptest.NewServer(tablePayload(t, `[[null],[120.0]]`))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true)
	candidates := straightLineCandidates()
	originalScoreA := candidates[0].score

	require.NotPanics(t, func() {
		e.rankByRoutedETA(context.Background(), "ride-5", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)
	})

	var a, b *candidate
	for _, c := range candidates {
		if c.profileID == "driver-A" {
			a = c
		} else {
			b = c
		}
	}
	require.NotNil(t, a)
	require.NotNil(t, b)
	assert.Equal(t, originalScoreA, a.score, "a nil OSRM cell must leave that candidate's straight-line score untouched")
	assert.NotEqual(t, float64(0.10), b.score, "the routable candidate must be re-scored by ETA")
}

// ── row-count mismatch (malformed OSRM response) → falls back safely ───────

func TestRankByRoutedETA_RowCountMismatch_FallsBackToStraightLineOrder(t *testing.T) {
	// Only one row returned for two candidates/sources.
	srv := httptest.NewServer(tablePayload(t, `[[120.0]]`))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true)
	candidates := straightLineCandidates()
	wantOrder := []string{candidates[0].profileID, candidates[1].profileID}
	wantScores := []float64{candidates[0].score, candidates[1].score}

	require.NotPanics(t, func() {
		e.rankByRoutedETA(context.Background(), "ride-6", geo.Point{Lat: -1.95, Lng: 30.07}, candidates)
	})

	assert.Equal(t, wantOrder, []string{candidates[0].profileID, candidates[1].profileID})
	assert.Equal(t, wantScores, []float64{candidates[0].score, candidates[1].score})
}

// ── empty candidate list → no-op, no panic, no HTTP call ───────────────────

func TestRankByRoutedETA_EmptyCandidates_NoOp(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true)
	require.NotPanics(t, func() {
		e.rankByRoutedETA(context.Background(), "ride-7", geo.Point{Lat: -1.95, Lng: 30.07}, nil)
	})
	assert.False(t, called, "an empty candidate list must not make an OSRM call")
}
