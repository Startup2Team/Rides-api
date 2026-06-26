package fare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

type RouteService interface {
	GetRouteMetrics(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string) (distanceKM float64, durationMinutes int, found bool, err error)
}

type Handler struct {
	repo     *Repository
	routeSvc RouteService
}

func NewHandler(repo *Repository, routeSvc RouteService) *Handler {
	return &Handler{repo: repo, routeSvc: routeSvc}
}

func (h *Handler) ListPublicPricing(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ListPublicPricing(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]any{"vehicle_types": items})
}

func (h *Handler) FareEstimate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	transportType := q.Get("transport_type")
	if transportType == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "transport_type is required")
		return
	}

	parseFloat := func(key string) (float64, error) {
		var f float64
		if _, err := fmt.Sscanf(q.Get(key), "%f", &f); err != nil {
			return 0, err
		}
		return f, nil
	}
	pickupLat, err := parseFloat("pickup_lat")
	if err != nil || pickupLat < -90 || pickupLat > 90 {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid pickup_lat")
		return
	}
	pickupLng, err := parseFloat("pickup_lng")
	if err != nil || pickupLng < -180 || pickupLng > 180 {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid pickup_lng")
		return
	}
	destLat, err := parseFloat("dest_lat")
	if err != nil || destLat < -90 || destLat > 90 {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid dest_lat")
		return
	}
	destLng, err := parseFloat("dest_lng")
	if err != nil || destLng < -180 || destLng > 180 {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid dest_lng")
		return
	}

	distanceKM, durationMinutes, found, err := h.routeSvc.GetRouteMetrics(r.Context(), pickupLat, pickupLng, destLat, destLng, transportType)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if !found {
		respond.Error(w, apperrors.New(http.StatusNotFound, "ROUTE_NOT_FOUND", "route not found"))
		return
	}

	cfg, err := h.repo.GetActiveConfig(r.Context(), transportType)
	if err != nil {
		respond.Error(w, apperrors.New(http.StatusNotFound, "PRICING_NOT_FOUND", "pricing config not found"))
		return
	}
	b := Calculate(cfg, distanceKM, time.Now(), 0)
	note := ""
	if cfg.NightSurchargePct > 0 {
		note = fmt.Sprintf("Night surcharge of %.0f%% applies after %02d:00", cfg.NightSurchargePct*100, cfg.NightStartHour)
	}

	respond.OK(w, map[string]any{
		"transport_type":       transportType,
		"distance_km":          distanceKM,
		"duration_minutes":     durationMinutes,
		"breakdown":            b,
		"min_fare_rwf":         cfg.MinFareRWF,
		"night_surcharge_pct":  cfg.NightSurchargePct,
		"night_start_hour":     cfg.NightStartHour,
		"night_end_hour":       cfg.NightEndHour,
		"waiting_rwf_per_min":  cfg.WaitingRWFPerMin,
		"waiting_free_minutes": cfg.WaitingFreeMinutes,
		"cancellation_fee_rwf": cfg.CancellationFeeRWF,
		"note":                 note,
	})
}

func (h *Handler) ListActivePricing(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ListActiveConfigs(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]any{"pricing": items})
}

func (h *Handler) GetActivePricingByType(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "vehicle_type_code")
	item, err := h.repo.GetActiveConfig(r.Context(), code)
	if err != nil {
		respond.Error(w, apperrors.ErrNotFound)
		return
	}
	respond.OK(w, item)
}

func (h *Handler) GetPricingHistory(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "vehicle_type_code")
	items, err := h.repo.GetConfigHistory(r.Context(), code)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]any{"history": items})
}

func (h *Handler) CreatePricing(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	code := chi.URLParam(r, "vehicle_type_code")
	var body Config
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	body.VehicleTypeCode = code
	if err := validateConfig(&body); err != nil {
		respond.Error(w, err)
		return
	}
	created, err := h.repo.CreateConfig(r.Context(), &body, claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, created)
}

func validateConfig(c *Config) error {
	if c.BaseFareRWF <= 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "base_fare_rwf must be > 0")
	}
	if c.BaseDistanceKM <= 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "base_distance_km must be > 0")
	}
	if c.Tier1PerKmRWF <= 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "tier1_per_km_rwf must be > 0")
	}
	if c.Tier1MaxKM <= c.BaseDistanceKM {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "tier1_max_km must be > base_distance_km")
	}
	if c.Tier2PerKmRWF <= 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "tier2_per_km_rwf must be > 0")
	}
	if c.NightSurchargePct < 0 || c.NightSurchargePct > 1.0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "night_surcharge_pct must be between 0 and 1")
	}
	if c.NightStartHour < 0 || c.NightStartHour > 23 || c.NightEndHour < 0 || c.NightEndHour > 23 || c.NightStartHour == c.NightEndHour {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "night_start_hour and night_end_hour must be in [0,23] and not equal")
	}
	if c.WaitingRWFPerMin < 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "waiting_rwf_per_min must be >= 0")
	}
	if c.WaitingFreeMinutes < 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "waiting_free_minutes must be >= 0")
	}
	if c.MinFareRWF < c.BaseFareRWF {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "min_fare_rwf must be >= base_fare_rwf")
	}
	if c.CancellationFeeRWF < 0 {
		return apperrors.New(http.StatusBadRequest, "VALIDATION", "cancellation_fee_rwf must be >= 0")
	}
	return nil
}
