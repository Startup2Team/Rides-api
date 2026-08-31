package routing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/routing"
	"github.com/workspace/ride-platform/pkg/geo"
)

func testCfg(url string) *config.Config {
	cfg := &config.Config{}
	cfg.Routing.OSRMURL = url
	return cfg
}

// ── Enabled() / feature-flag gating ─────────────────────────────────────────

func TestClient_Enabled_EmptyURL(t *testing.T) {
	c := routing.New(testCfg(""), zerolog.Nop())
	assert.False(t, c.Enabled(), "OSRM_URL unset must disable the client")
}

func TestClient_Enabled_URLSet(t *testing.T) {
	c := routing.New(testCfg("http://osrm.local:5000"), zerolog.Nop())
	assert.True(t, c.Enabled())
}

func TestClient_Route_DisabledReturnsError(t *testing.T) {
	c := routing.New(testCfg(""), zerolog.Nop())
	_, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.95, Lng: 30.10})
	require.Error(t, err, "a disabled client must fail closed, not panic or silently succeed")
}

func TestClient_Table_DisabledReturnsError(t *testing.T) {
	c := routing.New(testCfg(""), zerolog.Nop())
	_, err := c.Table(context.Background(), []geo.Point{{Lat: -1.94, Lng: 30.06}}, []geo.Point{{Lat: -1.95, Lng: 30.10}})
	require.Error(t, err)
}

// ── Route(): valid OSRM response parses into distance/duration/geometry ────

func TestClient_Route_ValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/route/v1/driving/")
		assert.Equal(t, "full", r.URL.Query().Get("overview"))
		assert.Equal(t, "polyline", r.URL.Query().Get("geometries"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": "Ok",
			"routes": [
				{"distance": 5230.4, "duration": 612.8, "geometry": "_p~iF~ps|U_ulLnnqC_mqNvxq"}
			]
		}`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	require.True(t, c.Enabled())

	result, err := c.Route(context.Background(), geo.Point{Lat: -1.9441, Lng: 30.0619}, geo.Point{Lat: -1.9355, Lng: 30.1127})
	require.NoError(t, err)
	assert.Equal(t, 5230.4, result.DistanceMeters)
	assert.Equal(t, 612.8, result.DurationSec)
	assert.Equal(t, "_p~iF~ps|U_ulLnnqC_mqNvxq", result.Geometry)
}

// ── Route(): OSRM 500 / "NoRoute" must surface as an error the caller
// treats as "fall back to Haversine" — never a parsed zero-value result ──

func TestClient_Route_ServerError_FallsBackToError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"InternalError","message":"boom"}`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	_, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.95, Lng: 30.10})
	require.Error(t, err, "a 500 from OSRM must be surfaced as an error, not a zero-value success")
}

func TestClient_Route_NoRouteCode_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"NoRoute","routes":[]}`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	_, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.95, Lng: 30.10})
	require.Error(t, err, "code != Ok must be an error even on HTTP 200")
}

func TestClient_Route_MalformedJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	_, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.95, Lng: 30.10})
	require.Error(t, err)
}

// ── Route(): a slow OSRM must time out rather than hang the caller ─────────

func TestClient_Route_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // longer than the client's bounded timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())

	start := time.Now()
	_, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.95, Lng: 30.10})
	elapsed := time.Since(start)

	require.Error(t, err, "a hung OSRM must time out, not block forever")
	assert.Less(t, elapsed, 3*time.Second, "the client's own bounded timeout must fire before the 3s server sleep")
}

// ── Route(): an empty/short geometry must not crash the caller ─────────────

func TestClient_Route_EmptyGeometry_Safe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":0,"duration":0,"geometry":""}]}`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	result, err := c.Route(context.Background(), geo.Point{Lat: -1.94, Lng: 30.06}, geo.Point{Lat: -1.94, Lng: 30.06})
	require.NoError(t, err)
	assert.Equal(t, "", result.Geometry)
	assert.Equal(t, 0.0, result.DistanceMeters)
}

// ── Table(): valid response parses into the duration/distance matrix ───────

func TestClient_Table_ValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/table/v1/driving/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": "Ok",
			"durations": [[0, 120.5], [340.2, 0]],
			"distances": [[0, 1500.0], [3200.0, 0]]
		}`))
	}))
	defer srv.Close()

	c := routing.New(testCfg(srv.URL), zerolog.Nop())
	sources := []geo.Point{{Lat: -1.94, Lng: 30.06}, {Lat: -1.95, Lng: 30.07}}
	dests := []geo.Point{{Lat: -1.94, Lng: 30.06}, {Lat: -1.95, Lng: 30.07}}

	result, err := c.Table(context.Background(), sources, dests)
	require.NoError(t, err)
	require.Len(t, result.DurationsSec, 2)
	assert.Equal(t, 120.5, *result.DurationsSec[0][1])
	assert.Equal(t, 3200.0, *result.DistancesMeters[1][0])
}

func TestClient_Table_EmptyInputs_ReturnsError(t *testing.T) {
	c := routing.New(testCfg("http://osrm.local:5000"), zerolog.Nop())
	_, err := c.Table(context.Background(), nil, []geo.Point{{Lat: -1.94, Lng: 30.06}})
	require.Error(t, err, "no sources must be rejected before any HTTP call")

	_, err = c.Table(context.Background(), []geo.Point{{Lat: -1.94, Lng: 30.06}}, nil)
	require.Error(t, err, "no destinations must be rejected before any HTTP call")
}

// ── Match(): stubbed, must not panic ────────────────────────────────────────

func TestClient_Match_Stub(t *testing.T) {
	c := routing.New(testCfg("http://osrm.local:5000"), zerolog.Nop())
	err := c.Match(context.Background(), []geo.Point{{Lat: -1.94, Lng: 30.06}})
	require.Error(t, err, "Match is stubbed and must fail closed, not silently succeed")
}
