// Package routing is a thin, typed client over a self-hosted OSRM instance
// (http://project-osrm.org/docs/v5.24.0/api/). It exists to turn straight-line
// (Haversine) distance/ETA estimates into real-road numbers where callers opt
// in.
//
// This capability is entirely optional and additive: Client.Enabled reports
// false whenever OSRM_URL is unset, and every method fails closed on ANY
// transport/parse/OSRM-side error. Callers MUST treat a non-nil error (or
// Enabled()==false) as "fall back to the existing Haversine estimate" — an
// OSRM outage or missing config must never fail a ride or a fare (see
// TEAM_CONTEXT.md platform invariants).
package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/pkg/geo"
)

// requestTimeout bounds every OSRM HTTP call. Routing is a nice-to-have on the
// booking/fare hot path — it must never hold a request hostage waiting on a
// slow or wedged OSRM instance.
const requestTimeout = 2 * time.Second

// Client is a bounded-timeout HTTP client over the OSRM HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
	log     zerolog.Logger
}

// New builds a Client from config. baseURL is read once at startup
// (cfg.Routing.OSRMURL / OSRM_URL); Enabled() reports whether it was set.
func New(cfg *config.Config, log zerolog.Logger) *Client {
	base := ""
	if cfg != nil {
		base = strings.TrimSuffix(strings.TrimSpace(cfg.Routing.OSRMURL), "/")
	}
	return &Client{
		baseURL: base,
		http:    &http.Client{Timeout: requestTimeout},
		log:     log,
	}
}

// Enabled reports whether OSRM_URL is configured. Callers must check this (or
// treat any error the same way) and fall back to Haversine when false —
// routing is an optional accelerant, never a hard dependency.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

// RouteResult is a real-road driving route from OSRM's /route service.
type RouteResult struct {
	DistanceMeters float64
	DurationSec    float64
	// Geometry is the route path as an encoded polyline (OSRM default:
	// precision-5 Google polyline encoding) — passed through as-is for the
	// client to decode and draw. Empty when OSRM returned no geometry (e.g.
	// origin == destination on some OSRM builds).
	Geometry string
}

// Route asks OSRM for the fastest driving route between origin and dest.
// Returns an error on ANY transport/parse/OSRM-side failure, config-off
// included — callers must treat that as "use the Haversine estimate instead",
// never as a request failure.
func (c *Client) Route(ctx context.Context, origin, dest geo.Point) (RouteResult, error) {
	if !c.Enabled() {
		return RouteResult{}, fmt.Errorf("routing: OSRM not configured")
	}

	url := fmt.Sprintf("%s/route/v1/driving/%s;%s?overview=full&geometries=polyline",
		c.baseURL, coordParam(origin), coordParam(dest))

	body, err := c.get(ctx, url)
	if err != nil {
		return RouteResult{}, fmt.Errorf("routing: route request: %w", err)
	}

	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Routes  []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry string  `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return RouteResult{}, fmt.Errorf("routing: decode route response: %w", err)
	}
	if out.Code != "Ok" || len(out.Routes) == 0 {
		return RouteResult{}, fmt.Errorf("routing: no route found (code=%s message=%s)", out.Code, out.Message)
	}

	best := out.Routes[0]
	return RouteResult{
		DistanceMeters: best.Distance,
		DurationSec:    best.Duration,
		Geometry:       best.Geometry,
	}, nil
}

// TableResult is the N×M travel-time (and, when OSRM returns it, distance)
// matrix from OSRM's /table service.
type TableResult struct {
	// DurationsSec[i][j] is the travel time in seconds from sources[i] to
	// destinations[j]. nil entries mean OSRM found no route for that pair.
	DurationsSec [][]*float64
	// DistancesMeters[i][j] mirrors DurationsSec for distance. May be nil
	// entirely on older OSRM builds that don't return distance annotations.
	DistancesMeters [][]*float64
}

// Table asks OSRM for the travel-time/distance matrix between every source
// and every destination in one call — used to rank N candidate drivers
// against a pickup point by real road ETA instead of straight-line distance.
// Kept to a single Redis-cheap HTTP call so it's safe to use from a matching
// hot loop; callers still must not call it more than once per ranking pass.
//
// Intentional scaffolding: no caller yet — this is wired up by the upcoming
// Phase 5 batched-matching work (ranking candidate drivers to a pickup by
// real road ETA), not dead code.
func (c *Client) Table(ctx context.Context, sources, dests []geo.Point) (TableResult, error) {
	if !c.Enabled() {
		return TableResult{}, fmt.Errorf("routing: OSRM not configured")
	}
	if len(sources) == 0 || len(dests) == 0 {
		return TableResult{}, fmt.Errorf("routing: table needs at least one source and one destination")
	}

	// OSRM /table takes ONE coordinate list plus index sets into it for
	// sources and destinations (they may overlap or be disjoint).
	coords := make([]string, 0, len(sources)+len(dests))
	srcIdx := make([]string, len(sources))
	for i, p := range sources {
		coords = append(coords, coordParam(p))
		srcIdx[i] = strconv.Itoa(i)
	}
	destIdx := make([]string, len(dests))
	for j, p := range dests {
		coords = append(coords, coordParam(p))
		destIdx[j] = strconv.Itoa(len(sources) + j)
	}

	url := fmt.Sprintf("%s/table/v1/driving/%s?sources=%s&destinations=%s&annotations=duration,distance",
		c.baseURL, strings.Join(coords, ";"), strings.Join(srcIdx, ";"), strings.Join(destIdx, ";"))

	body, err := c.get(ctx, url)
	if err != nil {
		return TableResult{}, fmt.Errorf("routing: table request: %w", err)
	}

	var out struct {
		Code      string       `json:"code"`
		Message   string       `json:"message"`
		Durations [][]*float64 `json:"durations"`
		Distances [][]*float64 `json:"distances"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return TableResult{}, fmt.Errorf("routing: decode table response: %w", err)
	}
	if out.Code != "Ok" {
		return TableResult{}, fmt.Errorf("routing: table failed (code=%s message=%s)", out.Code, out.Message)
	}

	return TableResult{DurationsSec: out.Durations, DistancesMeters: out.Distances}, nil
}

// Match performs snap-to-road / map-matching for a GPS trace (OSRM's /match
// service). Stubbed — no caller needs it yet; wire up when the tracking
// package snaps live driver traces to the road network.
func (c *Client) Match(ctx context.Context, trace []geo.Point) error {
	return fmt.Errorf("routing: Match not implemented")
}

// get issues a bounded-timeout GET and returns the response body, failing
// closed on any transport error or non-2xx status.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osrm %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// coordParam formats a point as OSRM expects: "lng,lat".
func coordParam(p geo.Point) string {
	return fmt.Sprintf("%f,%f", p.Lng, p.Lat)
}
