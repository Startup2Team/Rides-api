package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

	"github.com/workspace/ride-platform/internal/analytics"
)

// DriverPayoutRate is the share of the agreed fare the driver keeps. Our model
// is package-based (drivers buy ride credits and spend one at fare agreement) —
// there is NO per-ride commission, so the driver keeps 100% of the fare. This is
// the knob to change if a commission model is ever introduced.
const DriverPayoutRate = 1.0

// LocationUpdate is a single GPS update from the driver.
type LocationUpdate struct {
	Lat      float64
	Lng      float64
	SpeedKMH *float64
	Heading  *float64
}

// ApplyInput holds all fields for a driver application.
type ApplyInput struct {
	UserID                  string
	TransportType           string
	VehiclePlate            string
	LicenseNumber           string
	DateOfBirth             time.Time
	City                    string
	MomoPayCode             string
	MomoProvider            string
	Province                string
	District                string
	Sector                  string
	Cell                    string
	Village                 string
	PassengerSeats          *int
	LoadCapacityKg          *int
	LicenseExpiryDate       *time.Time
	InsuranceExpiryDate     *time.Time
	AuthorizationExpiryDate *time.Time
}

type CreditChecker interface {
	HasCredits(ctx context.Context, driverUserID, vehicleType string) (bool, error)
}

// Service handles driver business logic.
type Service struct {
	repo          *Repository
	redis         *goredis.Client
	analytics     *analytics.Service
	cfg           *config.Config
	log           zerolog.Logger
	creditChecker CreditChecker
}

func NewService(repo *Repository, rdb *goredis.Client, ana *analytics.Service, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{repo: repo, redis: rdb, analytics: ana, cfg: cfg, log: log}
}

func (s *Service) SetCreditChecker(cc CreditChecker) {
	s.creditChecker = cc
}

// Apply submits a driver application.
// In dev mode (DEV_AUTO_APPROVE_DRIVERS=true) the profile is immediately
// approved and the user's role_state is promoted to DRIVER_ACTIVE so
// they can go online without waiting for an admin action.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (*Profile, error) {
	existing, err := s.repo.FindProfileByUserID(ctx, in.UserID)
	if err == nil {
		// Profile already exists.
		if existing.ApprovalStatus == "REJECTED" {
			// Profile was previously rejected; allow resubmission.
			if rerr := s.repo.UpdateProfileForResubmission(ctx, in); rerr != nil {
				if isUniqueViolation(rerr) {
					return nil, apperrors.New(409, "DUPLICATE_CREDENTIALS", "vehicle plate or license number already registered")
				}
				return nil, rerr
			}

			if s.cfg.Driver.DevAutoApprove {
				if aerr := s.repo.SetApprovalStatus(ctx, existing.ID, "APPROVED", "dev-auto-approve", nil); aerr != nil {
					return nil, fmt.Errorf("dev auto-approve: %w", aerr)
				}
				if aerr := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); aerr != nil {
					return nil, fmt.Errorf("update role state: %w", aerr)
				}
				s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: resubmitted driver approved instantly")
			} else {
				if aerr := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_PENDING"); aerr != nil {
					return nil, fmt.Errorf("update role state: %w", aerr)
				}
			}
			return s.repo.FindProfileByUserID(ctx, in.UserID)
		}

		if s.cfg.Driver.DevAutoApprove && existing.ApprovalStatus != "APPROVED" {
			// Dev shortcut: approve the pending profile so the caller can proceed.
			if aerr := s.repo.SetApprovalStatus(ctx, existing.ID, "APPROVED", "", nil); aerr != nil {
				return nil, fmt.Errorf("dev auto-approve existing profile: %w", aerr)
			}
			if aerr := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); aerr != nil {
				return nil, fmt.Errorf("update role state: %w", aerr)
			}
			existing.ApprovalStatus = "APPROVED"
			s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: existing pending profile approved")
			return existing, nil
		}
		return nil, apperrors.ErrDriverAlreadyApplied
	}

	profile, err := s.repo.CreateProfile(ctx, in)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apperrors.New(409, "DUPLICATE_CREDENTIALS", "vehicle plate or license number already registered")
		}
		return nil, err
	}

	if s.cfg.Driver.DevAutoApprove {
		// Skip admin queue — approve immediately for dev/testing.
		if err := s.repo.SetApprovalStatus(ctx, profile.ID, "APPROVED", "dev-auto-approve", nil); err != nil {
			return nil, fmt.Errorf("dev auto-approve: %w", err)
		}
		if err := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_ACTIVE"); err != nil {
			return nil, fmt.Errorf("update role state: %w", err)
		}
		profile.ApprovalStatus = "APPROVED"
		s.log.Warn().Str("user_id", in.UserID).Msg("DEV_AUTO_APPROVE_DRIVERS: driver approved instantly — disable in production")
	} else {
		if err := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_PENDING"); err != nil {
			return nil, fmt.Errorf("update role state: %w", err)
		}
	}

	return profile, nil
}

// UpdateProfile updates mutable driver profile fields.
func (s *Service) UpdateProfile(ctx context.Context, userID string, city, momoPayCode, momoProvider, fcmToken *string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.UpdateProfileFields(ctx, profile.ID, city, momoPayCode, momoProvider, fcmToken)
}

// AcceptPolicy marks the driver policy as accepted.
func (s *Service) AcceptPolicy(ctx context.Context, userID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.SetPolicyAccepted(ctx, profile.ID)
}

// UploadDocument upserts a driver document record (URL only — file hosting is external).
func (s *Service) UploadDocument(ctx context.Context, userID, documentType, fileURL string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.repo.UpsertDocument(ctx, profile.ID, documentType, fileURL)
}

// ListDocuments returns all uploaded documents for a driver.
func (s *Service) ListDocuments(ctx context.Context, userID string) ([]*Document, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListDocuments(ctx, profile.ID)
}

// ForceOffline sets a driver OFFLINE unconditionally, ignoring any active-ride
// guard and cooldown. Used during logout so the driver is always cleanly removed
// from the matching pool even if their Redis state is stale.
func (s *Service) ForceOffline(ctx context.Context, userID string) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		// Not a driver — nothing to do.
		return
	}
	s.redis.Del(ctx, rkeys.K.DriverActiveRide(profile.ID))
	s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "OFFLINE", 0)
	s.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), profile.ID)
	_ = s.repo.UpdateOnlineStatus(ctx, userID, false)
	s.log.Info().Str("driver_id", profile.ID).Msg("driver: force-offlined on logout")
}

// SetAvailability toggles a driver online/offline with cooldown enforcement.
func (s *Service) SetAvailability(ctx context.Context, userID string, isOnline bool) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if isOnline {
		if profile.ApprovalStatus != "APPROVED" {
			return apperrors.ErrDriverNotActive
		}

		// 1. License Expiry Check
		if profile.LicenseExpiryDate != nil && profile.LicenseExpiryDate.Before(time.Now()) {
			return apperrors.New(http.StatusBadRequest, "EXPIRED_LICENSE", "Your driver license has expired. Update your driver license documents to continue.")
		}

		// 2. Active Package / Credits Check
		if s.creditChecker != nil {
			hasCredits, err := s.creditChecker.HasCredits(ctx, userID, profile.TransportType)
			if err != nil {
				return err
			}
			if !hasCredits {
				return apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding.")
			}
		}
		offlineKey := rkeys.K.DriverOfflineAt(profile.ID)
		_, redisErr := s.redis.Get(ctx, offlineKey).Result()
		if redisErr == nil {
			s.log.Info().Str("driver_id", profile.ID).Msg("driver came online within cooldown — penalties preserved")
		}
		// Always clear stale location history and reset the plausibility window,
		// even on an app-restart session-restore (driverProfile.isOnline=true path).
		// Without this the first HTTP location update after restart compares against
		// an old session's position and gets rejected as GPS_PLAUSIBILITY.
		s.redis.Del(ctx, rkeys.K.DriverLocationHistory(profile.ID))
		s.redis.Del(ctx, rkeys.K.GPSAnomalyCount(profile.ID))
		s.redis.Set(ctx, rkeys.K.DriverGracePeriod(profile.ID), "1", 60*time.Second)

		// If the driver has an active ride (e.g. app restarted mid-trip), preserve
		// the ON_TRIP Redis state so the matching engine doesn't re-pool them.
		// We still refresh the grace period above so location updates don't fail.
		activeRide, _ := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if activeRide == "" {
			// No active ride — full online transition.
			s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "AVAILABLE", 0)
			s.analytics.Publish(ctx, "driver.went_online", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
		}
	} else {
		// Verify no active ride before going offline.
		// Cross-check Redis against the DB: if the Redis key is stale (ride already
		// completed/cancelled) we clean it up and allow the offline transition.
		// This prevents the driver from being permanently locked offline when
		// a CompleteRide Redis write failed silently after the DB write succeeded.
		activeRide, _ := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if activeRide != "" {
			// Redis says active — verify the ride is actually still open in the DB.
			hasActiveInDB := s.repo.HasActiveRide(ctx, userID)
			if hasActiveInDB {
				return apperrors.New(409, "ACTIVE_RIDE", "complete your active ride before going offline")
			}
			// Stale Redis key — ride is done in DB. Clean up and continue.
			s.redis.Del(ctx, rkeys.K.DriverActiveRide(profile.ID))
			s.log.Warn().Str("driver_id", profile.ID).Str("stale_ride_id", activeRide).Msg("driver: cleaned up stale active_ride Redis key on offline transition")
		}
		offlineKey := rkeys.K.DriverOfflineAt(profile.ID)
		s.redis.Set(ctx, offlineKey, time.Now().UTC().Format(time.RFC3339),
			time.Duration(s.cfg.Driver.OfflineCooldownMinutes)*time.Minute)
		s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "OFFLINE", 0)
		s.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), profile.ID)
		s.analytics.Publish(ctx, "driver.went_offline", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
	}

	return s.repo.UpdateOnlineStatus(ctx, userID, isOnline)
}

// UpdateLocation processes a GPS update: plausibility check, Redis write, DB write.
func (s *Service) UpdateLocation(ctx context.Context, userID string, update LocationUpdate) error {
	newPoint := geo.Point{Lat: update.Lat, Lng: update.Lng}
	if err := newPoint.Validate(); err != nil {
		return err
	}

	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if !profile.IsOnline || profile.ApprovalStatus != "APPROVED" {
		return apperrors.ErrDriverNotActive
	}

	if anomaly, speed := s.checkGPSPlausibility(ctx, profile.ID, newPoint); anomaly {
		_ = s.repo.LogGPSAnomaly(ctx, profile.ID, speed, nil, &newPoint)

		anomalyKey := rkeys.K.GPSAnomalyCount(profile.ID)
		count, _ := s.redis.Incr(ctx, anomalyKey).Result()
		s.redis.Expire(ctx, anomalyKey, 8*time.Hour)

		s.analytics.Publish(ctx, "gps.anomaly_detected", "DRIVER", userID, nil, map[string]interface{}{
			"driver_id":          profile.ID,
			"computed_speed_kmh": speed,
		})

		if count >= 3 {
			_ = s.repo.SetApprovalStatus(ctx, profile.ID, "SUSPENDED", "", nil)
			s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-suspended: 3 GPS anomalies")
		}

		return apperrors.ErrGPSPlausibility
	}

	locJSON, _ := json.Marshal(map[string]interface{}{
		"lat":        update.Lat,
		"lng":        update.Lng,
		"speed_kmh":  update.SpeedKMH,
		"heading":    update.Heading,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
	// 120s TTL: clients now throttle pings (idle drivers send a heartbeat only
	// every ~60s), so a 30s TTL would let the location key expire between
	// heartbeats. 120s leaves headroom for a missed beat before the geofence
	// has to fall back to the PostGIS location.
	s.redis.Set(ctx, rkeys.K.DriverLocation(profile.ID), locJSON, 120*time.Second)
	s.redis.LPush(ctx, rkeys.K.DriverLocationHistory(profile.ID), locJSON)
	s.redis.LTrim(ctx, rkeys.K.DriverLocationHistory(profile.ID), 0, 9)

	// Re-assert the driver's presence in the Redis GEO index on EVERY ping.
	//
	// We used to skip this when movement was < 15m (a write-saving "noise
	// filter"), but that left a parked-yet-online driver invisible: if their
	// geo entry was ever dropped (trip handoff, Redis restart/eviction, manual
	// flush) while stationary, no subsequent ping would re-add them and they
	// became unmatchable despite being online. GeoAdd is O(log N) and
	// idempotent for an unchanged position, so always re-adding is the correct
	// trade-off — an online driver who is pinging is always discoverable.
	s.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), &goredis.GeoLocation{
		Name:      profile.ID,
		Longitude: update.Lng,
		Latitude:  update.Lat,
	})

	_ = s.repo.UpsertLocation(ctx, profile.ID, newPoint, update.SpeedKMH, update.Heading)
	return nil
}

func (s *Service) checkGPSPlausibility(ctx context.Context, driverProfileID string, newPoint geo.Point) (bool, float64) {
	// Outside production, skip plausibility entirely. Developers routinely
	// teleport the simulator (e.g. Cupertino → Kigali), which would otherwise
	// compute an impossible speed, flag a false anomaly, and eventually
	// auto-suspend the test driver. The guard stays fully active in production.
	if s.cfg.Env != "production" {
		return false, 0
	}

	// Skip the check entirely during the go-online grace period (first ~60 s).
	// The mobile app sends the placeholder KIGALI_CENTER position before real
	// device GPS resolves; comparing that to the actual GPS coordinates would
	// compute a physically impossible speed and trigger a false anomaly.
	if _, err := s.redis.Get(ctx, rkeys.K.DriverGracePeriod(driverProfileID)).Result(); err == nil {
		return false, 0
	}

	entries, err := s.redis.LRange(ctx, rkeys.K.DriverLocationHistory(driverProfileID), 0, 0).Result()
	if err != nil || len(entries) == 0 {
		return false, 0
	}
	var prev struct {
		Lat       float64 `json:"lat"`
		Lng       float64 `json:"lng"`
		UpdatedAt string  `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(entries[0]), &prev); err != nil {
		return false, 0
	}
	prevPoint := geo.Point{Lat: prev.Lat, Lng: prev.Lng}
	prevTime, err := time.Parse(time.RFC3339, prev.UpdatedAt)
	if err != nil {
		return false, 0
	}
	elapsed := time.Since(prevTime).Seconds()
	if elapsed <= 0 {
		return false, 0
	}
	if s.cfg.GPS.StaleThresholdSeconds > 0 && elapsed > s.cfg.GPS.StaleThresholdSeconds {
		return false, 0
	}
	speed := geo.SpeedKMH(prevPoint, newPoint, elapsed)
	if speed > s.cfg.GPS.MaxSpeedKMH {
		return true, speed
	}
	return false, speed
}

// RecordDecline handles a driver declining a ride request, applying penalties.
func (s *Service) RecordDecline(ctx context.Context, userID string) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	key := rkeys.K.DriverDailyDeclines(profile.ID)
	count, _ := s.redis.Incr(ctx, key).Result()
	s.redis.ExpireAt(ctx, key, endOfDay())

	s.analytics.Publish(ctx, "driver.declined_request", "DRIVER", userID, nil, map[string]interface{}{
		"driver_id":           profile.ID,
		"daily_decline_count": count,
	})

	switch {
	case int(count) >= s.cfg.Driver.DeclineAutoOfflineThreshold:
		if err := s.repo.UpdateOnlineStatus(ctx, userID, false); err != nil {
			return err
		}
		s.analytics.Publish(ctx, "driver.auto_offline", "DRIVER", userID, nil, map[string]interface{}{
			"driver_id": profile.ID, "reason": "15 daily declines",
		})
		s.log.Warn().Str("driver_id", profile.ID).Msg("driver auto-offlined: 15 declines")

	case int(count) >= s.cfg.Driver.DeclinePriorityThreshold:
		if err := s.repo.SetPriorityTier(ctx, profile.ID, 2); err != nil {
			return err
		}
		s.analytics.Publish(ctx, "driver.priority_demoted", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
	}

	return nil
}

// GetProfile returns the current driver profile.
func (s *Service) GetProfile(ctx context.Context, userID string) (*Profile, error) {
	return s.repo.FindProfileByUserID(ctx, userID)
}

// GetDailyEarnings returns total fare revenue for today.
// GetDailyEarnings returns today's driver payout and the number of rides
// completed today.
func (s *Service) GetDailyEarnings(ctx context.Context, driverUserID string) (float64, int, error) {
	gross, count, err := s.repo.GetEarnings(ctx, driverUserID, "1 day")
	if err != nil {
		return 0, 0, err
	}
	return CalculateDriverPayout(gross), count, nil
}

// GetWeeklyEarnings returns total fare revenue for the last 7 days.
func (s *Service) GetWeeklyEarnings(ctx context.Context, driverUserID string) (float64, error) {
	gross, _, err := s.repo.GetEarnings(ctx, driverUserID, "7 days")
	if err != nil {
		return 0, err
	}
	return CalculateDriverPayout(gross), nil
}

func CalculateDriverPayout(grossFare float64) float64 {
	return grossFare * DriverPayoutRate
}

// GetStats returns driver performance statistics.
func (s *Service) GetStats(ctx context.Context, driverUserID string) (map[string]interface{}, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, driverUserID)
	if err != nil {
		return nil, err
	}

	completionRate, err := s.repo.GetCompletionRate(ctx, profile.ID)
	if err != nil {
		completionRate = 0
	}

	return map[string]interface{}{
		"total_rides":     profile.TotalRides,
		"acceptance_rate": profile.AcceptanceRate,
		"completion_rate": completionRate,
		"priority_tier":   profile.PriorityTier,
	}, nil
}

// allVehicleTypes lists every vehicle type the platform supports.
var allVehicleTypes = []string{"MOTO_BIKE", "CAB_TAXI", "HEAVY_FUSO", "LIGHT_HILUX"}

const (
	driverStateAvailable = "AVAILABLE"
	// The nearby-driver preview radius matches the matching engine's expanded
	// reach (10 km) so a driver the customer could actually be matched with also
	// shows on the map. A narrower preview made online drivers look absent.
	nearbySearchRadiusKM = 10.0
	nearbySearchRadiusM  = 10000
	nearbyMaxPerType     = 6
	// Kigali city-average speed used for ETA estimation without a routing call.
	citySpeedKMH = 25.0
)

// GetNearbyDrivers returns anonymised nearby drivers for a customer location.
// If transportType is empty, all vehicle types are queried in a single call.
//
// Primary source: Redis GEO (real-time, sub-millisecond per type).
// Fallback:       PostGIS driver_locations (cold-start, Redis flush).
//
// Each result includes an estimated ETA computed from straight-line distance
// at city-average speed — no routing API call needed.
func (s *Service) GetNearbyDrivers(ctx context.Context, loc geo.Point, transportType string) ([]*NearbyDriver, error) {
	types := allVehicleTypes
	if transportType != "" {
		types = []string{transportType}
	}

	var result []*NearbyDriver
	for _, tt := range types {
		drivers := s.nearbyForType(ctx, loc, tt)
		result = append(result, drivers...)
	}
	return result, nil
}

// nearbyForType queries one vehicle type: Redis GEO first, PostGIS fallback.
func (s *Service) nearbyForType(ctx context.Context, loc geo.Point, transportType string) []*NearbyDriver {
	// ── 1. Redis GEO — real-time, O(log N + k) ───────────────────────────────
	geoKey := rkeys.K.DriverGeoIndex(transportType)
	geoResults, err := s.redis.GeoSearchLocation(ctx, geoKey, &goredis.GeoSearchLocationQuery{
		GeoSearchQuery: goredis.GeoSearchQuery{
			Longitude:  loc.Lng,
			Latitude:   loc.Lat,
			Radius:     nearbySearchRadiusKM,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      nearbyMaxPerType + 4, // fetch extra to allow for state filtering
		},
		WithCoord: true,
		WithDist:  true,
	}).Result()

	if err == nil && len(geoResults) > 0 {
		var drivers []*NearbyDriver
		for _, r := range geoResults {
			if len(drivers) >= nearbyMaxPerType {
				break
			}
			// Skip drivers not in AVAILABLE state (ON_TRIP, OFFLINE, matching-locked).
			state, _ := s.redis.Get(ctx, rkeys.K.DriverState(r.Name)).Result()
			if state != driverStateAvailable {
				continue
			}
			distM := r.Dist * 1000
			drivers = append(drivers, &NearbyDriver{
				TransportType: transportType,
				DistanceM:     distM,
				ApproxLat:     r.Latitude + jitter(),
				ApproxLng:     r.Longitude + jitter(),
				ETAMinutes:    etaMinutes(distM),
			})
		}
		if len(drivers) > 0 {
			return drivers
		}
	}

	// ── 2. PostGIS fallback — handles cold-start / Redis flush ────────────────
	candidates, err := s.repo.FindNearby(ctx, loc, nearbySearchRadiusM, transportType, nil)
	if err != nil || len(candidates) == 0 {
		return nil
	}
	var fallback []*NearbyDriver
	for _, c := range candidates {
		if len(fallback) >= nearbyMaxPerType {
			break
		}
		fallback = append(fallback, &NearbyDriver{
			TransportType: transportType,
			DistanceM:     c.DistanceM,
			ApproxLat:     c.Lat + jitter(),
			ApproxLng:     c.Lng + jitter(),
			ETAMinutes:    etaMinutes(c.DistanceM),
		})
	}
	return fallback
}

// etaMinutes estimates arrival time from straight-line distance at city speed.
// Good enough for the pre-booking map view; avoids a routing API call per driver.
func etaMinutes(distanceM float64) int {
	if distanceM <= 0 {
		return 1
	}
	minutes := (distanceM / 1000.0) / citySpeedKMH * 60.0
	eta := int(math.Ceil(minutes))
	if eta < 1 {
		return 1
	}
	return eta
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "23505") || contains(msg, "unique")
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func endOfDay() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
}

// jitter adds a small random offset to driver coordinates before sending them
// to customers. This prevents customers from pinpointing a driver's exact location
// before a ride is booked (privacy), while keeping the map marker believable.
// ±0.0015° ≈ ±165 m per axis — large enough for privacy, small enough to stay
// within the same city block so the marker looks correct on the map.
func jitter() float64 {
	return (rand.Float64() - 0.5) * 0.003
}
