package location

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const (
	routeCacheTTL     = 24 * time.Hour
	suggestionsTTL    = 10 * time.Minute
	landmarksTTL      = 1 * time.Hour
	minFareDataPoints = 10
)

// RouteResult is returned from GetRoute.
type RouteResult struct {
	CacheKey        string  `json:"cache_key"`
	OriginGeohash   string  `json:"origin_geohash"`
	DestGeohash     string  `json:"dest_geohash"`
	DistanceKM      float64 `json:"distance_km"`
	DurationMinutes int     `json:"duration_minutes"`
	AvgFareRWF      *int    `json:"avg_fare_rwf,omitempty"`
	UseCount        int     `json:"use_count"`
}

// Landmark is a pre-seeded Kigali destination.
type Landmark struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Geohash6 string  `json:"geohash6"`
}

// SavedLocation is a user-saved place.
type SavedLocation struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Label     string    `json:"label"`
	Address   string    `json:"address"`
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Service handles route cache, landmarks, saved locations, suggestions, mode switching.
type Service struct {
	db    *pgxpool.Pool
	redis goredis.UniversalClient
	cfg   *config.Config
	log   zerolog.Logger
}

func NewService(db *pgxpool.Pool, rdb goredis.UniversalClient, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{db: db, redis: rdb, cfg: cfg, log: log}
}

// ── Route Cache ───────────────────────────────────────────────────────────

// GetRoute returns a cached route by geohash key, or nil if not cached.
func (s *Service) GetRoute(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string) (*RouteResult, error) {
	cacheKey := buildCacheKey(pickupLat, pickupLng, destLat, destLng, vehicleType)

	if cached, err := s.redis.Get(ctx, rkeys.K.RouteCache(cacheKey)).Result(); err == nil {
		var result RouteResult
		if json.Unmarshal([]byte(cached), &result) == nil {
			go s.incrementUseCount(cacheKey)
			return &result, nil
		}
	}

	result, err := s.getRouteFromDB(ctx, cacheKey)
	if err == nil {
		data, _ := json.Marshal(result)
		s.redis.Set(ctx, rkeys.K.RouteCache(cacheKey), string(data), routeCacheTTL)
		go s.incrementUseCount(cacheKey)
		return result, nil
	}

	return nil, nil
}

// GetRouteMetrics returns route distance/duration for fare calculations.
// If no cached route exists for the given coordinate pair, it falls back to a
// Haversine straight-line estimate (+20% road-factor) so the fare handler can
// always return a result. The caller receives found=true in both cases; the
// haversine path is flagged in the log for observability only.
func (s *Service) GetRouteMetrics(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string) (float64, int, bool, error) {
	result, err := s.GetRoute(ctx, pickupLat, pickupLng, destLat, destLng, vehicleType)
	if err != nil {
		return 0, 0, false, err
	}
	if result != nil {
		return result.DistanceKM, result.DurationMinutes, true, nil
	}

	// No cached route — compute a Haversine estimate so the fare endpoint always
	// responds. Apply a 1.25× road-factor (straight-line underestimates road
	// distance by ~20–25% in Kigali's hilly terrain).
	straightKM := geo.DistanceKM(
		geo.Point{Lat: pickupLat, Lng: pickupLng},
		geo.Point{Lat: destLat, Lng: destLng},
	)
	const roadFactor = 1.25
	estimatedKM := straightKM * roadFactor
	// Assume 30 km/h average speed in Kigali traffic + 3 min fixed overhead.
	estimatedMin := int(estimatedKM/30*60) + 3
	if estimatedMin < 1 {
		estimatedMin = 1
	}
	s.log.Debug().
		Float64("straight_km", straightKM).
		Float64("estimated_km", estimatedKM).
		Str("vehicle_type", vehicleType).
		Msg("location: route cache miss — using haversine estimate")

	return estimatedKM, estimatedMin, true, nil
}

// UpsertRoute stores a route result provided by the mobile app.
func (s *Service) UpsertRoute(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string, distanceKM float64, durationMinutes int) (*RouteResult, error) {
	originHash := Geohash6(pickupLat, pickupLng)
	destHash := Geohash6(destLat, destLng)
	cacheKey := fmt.Sprintf("%s:%s:%s", originHash, destHash, vehicleType)

	_, err := s.db.Exec(ctx, `
		INSERT INTO route_cache (cache_key, origin_geohash, dest_geohash, vehicle_type, distance_km, duration_minutes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (cache_key) DO UPDATE
		SET use_count = route_cache.use_count + 1, last_used_at = NOW()
	`, cacheKey, originHash, destHash, vehicleType, distanceKM, durationMinutes)
	if err != nil {
		return nil, err
	}

	result := &RouteResult{
		CacheKey: cacheKey, OriginGeohash: originHash, DestGeohash: destHash,
		DistanceKM: distanceKM, DurationMinutes: durationMinutes, UseCount: 1,
	}
	data, _ := json.Marshal(result)
	s.redis.Set(ctx, rkeys.K.RouteCache(cacheKey), string(data), routeCacheTTL)
	return result, nil
}

// RecordAgreedFare appends an agreed fare to the route cache for fare suggestions.
func (s *Service) RecordAgreedFare(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string, agreedFare float64) {
	cacheKey := buildCacheKey(pickupLat, pickupLng, destLat, destLng, vehicleType)
	_, err := s.db.Exec(ctx, `
		UPDATE route_cache
		SET agreed_fares = agreed_fares || $1::jsonb,
		    avg_fare_rwf = (
		        SELECT ROUND(AVG(val::numeric))
		        FROM jsonb_array_elements_text(agreed_fares || $1::jsonb) AS val
		    ),
		    last_used_at = NOW()
		WHERE cache_key = $2
	`, fmt.Sprintf("[%d]", int(agreedFare)), cacheKey)
	if err != nil {
		s.log.Warn().Err(err).Str("cache_key", cacheKey).Msg("route_cache: record fare failed")
		return
	}
	s.redis.Del(ctx, rkeys.K.RouteCache(cacheKey))
}

// GetFareSuggestion returns a fare range hint if enough data exists.
func (s *Service) GetFareSuggestion(ctx context.Context, pickupLat, pickupLng, destLat, destLng float64, vehicleType string) map[string]interface{} {
	cacheKey := buildCacheKey(pickupLat, pickupLng, destLat, destLng, vehicleType)
	var avgFare *int
	var useCount int
	err := s.db.QueryRow(ctx,
		`SELECT avg_fare_rwf, use_count FROM route_cache WHERE cache_key = $1`, cacheKey,
	).Scan(&avgFare, &useCount)
	if err != nil || avgFare == nil || useCount < minFareDataPoints {
		return nil
	}
	avg := *avgFare
	return map[string]interface{}{
		"min_rwf": int(float64(avg) * 0.8),
		"max_rwf": int(float64(avg) * 1.2),
		"avg_rwf": avg,
		"hint":    fmt.Sprintf("Most riders pay %d–%d RWF", int(float64(avg)*0.8), int(float64(avg)*1.2)),
	}
}

// ── Landmarks ─────────────────────────────────────────────────────────────

func (s *Service) GetLandmarks(ctx context.Context) ([]*Landmark, error) {
	if cached, err := s.redis.Get(ctx, rkeys.K.LandmarkSuggestions()).Result(); err == nil {
		var landmarks []*Landmark
		if json.Unmarshal([]byte(cached), &landmarks) == nil {
			return landmarks, nil
		}
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, name, category, lat, lng, geohash6 FROM landmarks ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var landmarks []*Landmark
	for rows.Next() {
		l := &Landmark{}
		if err := rows.Scan(&l.ID, &l.Name, &l.Category, &l.Lat, &l.Lng, &l.Geohash6); err != nil {
			return nil, err
		}
		landmarks = append(landmarks, l)
	}

	data, _ := json.Marshal(landmarks)
	s.redis.Set(ctx, rkeys.K.LandmarkSuggestions(), string(data), landmarksTTL)
	return landmarks, nil
}

// ── Saved Locations ───────────────────────────────────────────────────────

func (s *Service) ListSavedLocations(ctx context.Context, userID string) ([]*SavedLocation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, user_id, label, address, lat, lng, created_at, updated_at
		 FROM saved_locations WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locs []*SavedLocation
	for rows.Next() {
		l := &SavedLocation{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Label, &l.Address, &l.Lat, &l.Lng, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		locs = append(locs, l)
	}
	return locs, nil
}

func (s *Service) CreateSavedLocation(ctx context.Context, userID, label, address string, lat, lng float64) (*SavedLocation, error) {
	l := &SavedLocation{}
	err := s.db.QueryRow(ctx, `
		INSERT INTO saved_locations (user_id, label, address, lat, lng)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, label, address, lat, lng, created_at, updated_at
	`, userID, label, address, lat, lng).Scan(
		&l.ID, &l.UserID, &l.Label, &l.Address, &l.Lat, &l.Lng, &l.CreatedAt, &l.UpdatedAt,
	)
	return l, err
}

func (s *Service) UpdateSavedLocation(ctx context.Context, id, userID, label, address string, lat, lng float64) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE saved_locations
		SET label = $1, address = $2, lat = $3, lng = $4, updated_at = NOW()
		WHERE id = $5 AND user_id = $6
	`, label, address, lat, lng, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

func (s *Service) DeleteSavedLocation(ctx context.Context, id, userID string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE saved_locations SET deleted_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

// SavedLocationInput is one entry in a bulk replace.
type SavedLocationInput struct {
	Label   string
	Address string
	Lat     float64
	Lng     float64
}

// ReplaceSavedLocations atomically swaps ALL of a user's saved locations for the
// provided set inside a single transaction. This fixes the client's old
// sequential delete/update/create path, which could leave the server
// half-updated if one call failed mid-flight.
func (s *Service) ReplaceSavedLocations(ctx context.Context, userID string, in []SavedLocationInput) ([]*SavedLocation, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Soft-delete the current live set (kept as history), then insert the new one.
	if _, err := tx.Exec(ctx, `UPDATE saved_locations SET deleted_at = NOW() WHERE user_id = $1 AND deleted_at IS NULL`, userID); err != nil {
		return nil, err
	}
	for _, loc := range in {
		if _, err := tx.Exec(ctx, `
			INSERT INTO saved_locations (user_id, label, address, lat, lng)
			VALUES ($1, $2, $3, $4, $5)
		`, userID, loc.Label, loc.Address, loc.Lat, loc.Lng); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ListSavedLocations(ctx, userID)
}

// ── Admin units (Rwanda hierarchy) ───────────────────────────────────────────

// AdminUnit is one node in the province→village hierarchy.
type AdminUnit struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Level    string  `json:"level"`
	Name     string  `json:"name"`
	Path     string  `json:"path"`
}

func scanAdminUnits(rows pgx.Rows) ([]*AdminUnit, error) {
	defer rows.Close()
	units := make([]*AdminUnit, 0)
	for rows.Next() {
		u := &AdminUnit{}
		if err := rows.Scan(&u.ID, &u.ParentID, &u.Level, &u.Name, &u.Path); err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, rows.Err()
}

// ListAdminUnitChildren returns the direct children of a node, or the top-level
// provinces when parentID is empty. Alphabetical by name.
func (s *Service) ListAdminUnitChildren(ctx context.Context, parentID string) ([]*AdminUnit, error) {
	if parentID == "" {
		rows, err := s.db.Query(ctx,
			`SELECT id, parent_id, level, name, path FROM admin_units
			 WHERE parent_id IS NULL ORDER BY name`)
		if err != nil {
			return nil, err
		}
		return scanAdminUnits(rows)
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, parent_id, level, name, path FROM admin_units
		 WHERE parent_id = $1 ORDER BY name`, parentID)
	if err != nil {
		return nil, err
	}
	return scanAdminUnits(rows)
}

// SearchAdminUnits does prefix/substring autocomplete on unit names. An optional
// level filter (e.g. "village") narrows results. Capped for a snappy picker.
func (s *Service) SearchAdminUnits(ctx context.Context, q, level string, limit int) ([]*AdminUnit, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + q + "%"
	if level != "" {
		rows, err := s.db.Query(ctx,
			`SELECT id, parent_id, level, name, path FROM admin_units
			 WHERE level = $1 AND lower(name) LIKE lower($2)
			 ORDER BY name LIMIT $3`, level, pattern, limit)
		if err != nil {
			return nil, err
		}
		return scanAdminUnits(rows)
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, parent_id, level, name, path FROM admin_units
		 WHERE lower(name) LIKE lower($1)
		 ORDER BY name LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, err
	}
	return scanAdminUnits(rows)
}

// ── Recent locations ────────────────────────────────────────────────────────

// RecentLocation is a place the rider recently picked for a booking.
type RecentLocation struct {
	ID         string    `json:"id"`
	Address    string    `json:"address"`
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	UseCount   int       `json:"use_count"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// recentLocationsLimit caps how many recents we return / retain per user.
const recentLocationsLimit = 15

// RecordRecentLocation upserts a picked place into the rider's recents: a new
// (user,address) inserts; re-picking bumps last_used_at + use_count. Best-effort
// from the caller's view — a failure here must never block a booking. Invalidates
// the cached suggestions so the new recent shows immediately.
func (s *Service) RecordRecentLocation(ctx context.Context, userID, address string, lat, lng float64) error {
	if address == "" {
		return apperrors.ErrBadRequest
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO recent_locations (user_id, address, lat, lng)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, address) WHERE deleted_at IS NULL
		DO UPDATE SET last_used_at = NOW(), use_count = recent_locations.use_count + 1,
		              lat = EXCLUDED.lat, lng = EXCLUDED.lng
	`, userID, address, lat, lng)
	if err != nil {
		return err
	}
	s.redis.Del(ctx, rkeys.K.UserSuggestions(userID))
	return nil
}

// ListRecentLocations returns the rider's most-recent live places, capped.
func (s *Service) ListRecentLocations(ctx context.Context, userID string) ([]*RecentLocation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, address, lat, lng, use_count, last_used_at
		FROM recent_locations
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY last_used_at DESC
		LIMIT $2
	`, userID, recentLocationsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recents := make([]*RecentLocation, 0)
	for rows.Next() {
		r := &RecentLocation{}
		if err := rows.Scan(&r.ID, &r.Address, &r.Lat, &r.Lng, &r.UseCount, &r.LastUsedAt); err != nil {
			return nil, err
		}
		recents = append(recents, r)
	}
	return recents, rows.Err()
}

// DeleteRecentLocation soft-deletes one recent the caller owns (per the
// soft-delete rule — the row is kept, just hidden).
func (s *Service) DeleteRecentLocation(ctx context.Context, id, userID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE recent_locations SET deleted_at = NOW()
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
	`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	s.redis.Del(ctx, rkeys.K.UserSuggestions(userID))
	return nil
}

// ── Suggestions ───────────────────────────────────────────────────────────

func (s *Service) GetSuggestions(ctx context.Context, userID string) (map[string]interface{}, error) {
	if cached, err := s.redis.Get(ctx, rkeys.K.UserSuggestions(userID)).Result(); err == nil {
		var result map[string]interface{}
		if json.Unmarshal([]byte(cached), &result) == nil {
			return result, nil
		}
	}

	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (destination_address) destination_address,
		       ST_Y(destination_point::geometry) AS dest_lat,
		       ST_X(destination_point::geometry) AS dest_lng
		FROM rides
		WHERE customer_id = $1 AND status = 'COMPLETED'
		ORDER BY destination_address, created_at DESC
		LIMIT 5
	`, userID)
	var recentDests []map[string]interface{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var addr string
			var lat, lng float64
			if rows.Scan(&addr, &lat, &lng) == nil {
				recentDests = append(recentDests, map[string]interface{}{
					"address": addr, "lat": lat, "lng": lng,
				})
			}
		}
	}

	saved, _ := s.ListSavedLocations(ctx, userID)
	landmarks, _ := s.GetLandmarks(ctx)
	// Explicit recents (picked-for-booking); the ride-derived recent_destinations
	// stay as a fallback for users from before this feature existed.
	recents, _ := s.ListRecentLocations(ctx, userID)

	result := map[string]interface{}{
		"saved_locations":     saved,
		"recent_locations":    recents,
		"recent_destinations": recentDests,
		"landmarks":           landmarks,
	}

	data, _ := json.Marshal(result)
	s.redis.Set(ctx, rkeys.K.UserSuggestions(userID), string(data), suggestionsTTL)
	return result, nil
}

// ── Mode Switching ────────────────────────────────────────────────────────

func (s *Service) SwitchMode(ctx context.Context, userID, mode string) error {
	if mode != "customer" && mode != "driver" {
		return apperrors.ErrBadRequest
	}

	if mode == "driver" {
		var status string
		var policyAccepted bool
		err := s.db.QueryRow(ctx,
			`SELECT approval_status, COALESCE(policy_accepted, FALSE) FROM driver_profiles WHERE user_id = $1`,
			userID,
		).Scan(&status, &policyAccepted)
		if err != nil {
			return apperrors.New(403, "NO_DRIVER_PROFILE", "driver profile not found")
		}
		if status != "APPROVED" {
			return apperrors.New(403, "DRIVER_NOT_ACTIVE", "driver profile is not active")
		}
		if !policyAccepted {
			return apperrors.New(403, "POLICY_NOT_ACCEPTED", "driver must accept all policies first")
		}
	}

	var activeRideCount int
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides
		WHERE (customer_id = $1 OR driver_id = (SELECT id FROM driver_profiles WHERE user_id = $1))
		  AND status NOT IN ('COMPLETED','CANCELLED')
	`, userID).Scan(&activeRideCount)
	if activeRideCount > 0 {
		return apperrors.New(409, "ACTIVE_RIDE", "complete your active ride before switching modes")
	}

	roleState := "CUSTOMER_ONLY"
	if mode == "driver" {
		roleState = "DRIVER_ACTIVE"
	}
	_, err := s.db.Exec(ctx,
		`UPDATE users SET role_state = $1, updated_at = NOW() WHERE id = $2`, roleState, userID)
	if err != nil {
		return err
	}
	s.revokeUserSessions(ctx, userID)
	return nil
}

func (s *Service) revokeUserSessions(ctx context.Context, userID string) {
	iter := s.redis.Scan(ctx, 0, "session:"+userID+":*", 100).Iterator()
	for iter.Next(ctx) {
		s.redis.Del(ctx, iter.Val())
	}
}

// ── Startup warm ──────────────────────────────────────────────────────────

// WarmLandmarkRoutes pre-warms the route cache for common Kigali corridors.
func (s *Service) WarmLandmarkRoutes(ctx context.Context) {
	go func() {
		landmarks, err := s.GetLandmarks(ctx)
		if err != nil || len(landmarks) == 0 {
			return
		}
		vehicleTypes := []string{"MOTO_BIKE", "CAB_TAXI", "LIGHT_HILUX", "HEAVY_FUSO", "TUK_TUK"}
		limit := 5
		if len(landmarks) < limit {
			limit = len(landmarks)
		}
		for i := 0; i < limit; i++ {
			for j := 0; j < limit; j++ {
				if i == j {
					continue
				}
				a, b := landmarks[i], landmarks[j]
				distKM := geo.DistanceKM(geo.Point{Lat: a.Lat, Lng: a.Lng}, geo.Point{Lat: b.Lat, Lng: b.Lng})
				durationMin := int(distKM/30*60) + 5
				for _, vt := range vehicleTypes {
					_, _ = s.UpsertRoute(ctx, a.Lat, a.Lng, b.Lat, b.Lng, vt, distKM, durationMin)
				}
			}
		}
		s.log.Info().Msg("location: landmark route cache pre-warmed")
	}()
}

// ── Helpers ───────────────────────────────────────────────────────────────

func buildCacheKey(pickupLat, pickupLng, destLat, destLng float64, vehicleType string) string {
	return fmt.Sprintf("%s:%s:%s", Geohash6(pickupLat, pickupLng), Geohash6(destLat, destLng), vehicleType)
}

func (s *Service) getRouteFromDB(ctx context.Context, cacheKey string) (*RouteResult, error) {
	r := &RouteResult{}
	err := s.db.QueryRow(ctx, `
		SELECT cache_key, origin_geohash, dest_geohash, distance_km, duration_minutes, avg_fare_rwf, use_count
		FROM route_cache WHERE cache_key = $1
	`, cacheKey).Scan(&r.CacheKey, &r.OriginGeohash, &r.DestGeohash, &r.DistanceKM, &r.DurationMinutes, &r.AvgFareRWF, &r.UseCount)
	return r, err
}

func (s *Service) incrementUseCount(cacheKey string) {
	s.db.Exec(context.Background(),
		`UPDATE route_cache SET use_count = use_count + 1, last_used_at = NOW() WHERE cache_key = $1`, cacheKey)
}

// Geohash6 encodes lat/lng to geohash precision 6 (~1.2km cell).
func Geohash6(lat, lng float64) string {
	const base32 = "0123456789bcdefghjkmnpqrstuvwxyz"
	minLat, maxLat := -90.0, 90.0
	minLng, maxLng := -180.0, 180.0

	var hash [6]byte
	isLng := true
	bit := 4
	ch := 0

	for i := 0; i < 6; {
		if isLng {
			mid := (minLng + maxLng) / 2
			if lng >= mid {
				ch |= 1 << bit
				minLng = mid
			} else {
				maxLng = mid
			}
		} else {
			mid := (minLat + maxLat) / 2
			if lat >= mid {
				ch |= 1 << bit
				minLat = mid
			} else {
				maxLat = mid
			}
		}
		isLng = !isLng
		if bit == 0 {
			hash[i] = base32[ch]
			i++
			bit = 4
			ch = 0
		} else {
			bit--
		}
	}
	return string(hash[:])
}
