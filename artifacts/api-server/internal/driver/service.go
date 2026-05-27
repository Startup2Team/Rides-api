package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

	"github.com/workspace/ride-platform/internal/analytics"
)

const DriverPayoutRate = 0.85

// LocationUpdate is a single GPS update from the driver.
type LocationUpdate struct {
	Lat      float64
	Lng      float64
	SpeedKMH *float64
	Heading  *float64
}

// ApplyInput holds all fields for a driver application.
type ApplyInput struct {
	UserID         string
	TransportType  string
	VehiclePlate   string
	LicenseNumber  string
	DateOfBirth    time.Time
	City           string
	MomoPayCode    string
	MomoProvider   string
	Province       string
	District       string
	Sector         string
	Cell           string
	Village        string
	PassengerSeats *int
	LoadCapacityKg *int
}

// Service handles driver business logic.
type Service struct {
	repo      *Repository
	redis     *goredis.Client
	analytics *analytics.Service
	cfg       *config.Config
	log       zerolog.Logger
}

func NewService(repo *Repository, rdb *goredis.Client, ana *analytics.Service, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{repo: repo, redis: rdb, analytics: ana, cfg: cfg, log: log}
}

// Apply submits a driver application.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (*Profile, error) {
	_, err := s.repo.FindProfileByUserID(ctx, in.UserID)
	if err == nil {
		return nil, apperrors.ErrDriverAlreadyApplied
	}

	profile, err := s.repo.CreateProfile(ctx, in)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apperrors.New(409, "DUPLICATE_CREDENTIALS", "vehicle plate or license number already registered")
		}
		return nil, err
	}

	if err := s.repo.UpdateUserRoleState(ctx, in.UserID, "DRIVER_PENDING"); err != nil {
		return nil, fmt.Errorf("update role state: %w", err)
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

// SetAvailability toggles a driver online/offline with cooldown enforcement.
func (s *Service) SetAvailability(ctx context.Context, userID string, isOnline bool) error {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return err
	}

	if isOnline {
		offlineKey := rkeys.K.DriverOfflineAt(profile.ID)
		_, redisErr := s.redis.Get(ctx, offlineKey).Result()
		if redisErr == nil {
			s.log.Info().Str("driver_id", profile.ID).Msg("driver came online within cooldown — penalties preserved")
		}
		// Set driver state + add to GEO index
		s.redis.Set(ctx, rkeys.K.DriverState(profile.ID), "AVAILABLE", 0)
		s.analytics.Publish(ctx, "driver.went_online", "DRIVER", userID, nil, map[string]interface{}{"driver_id": profile.ID})
	} else {
		// Verify no active ride before going offline
		activeRide, _ := s.redis.Get(ctx, rkeys.K.DriverActiveRide(profile.ID)).Result()
		if activeRide != "" {
			return apperrors.New(409, "ACTIVE_RIDE", "complete your active ride before going offline")
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
	s.redis.Set(ctx, rkeys.K.DriverLocation(profile.ID), locJSON, 30*time.Second)
	s.redis.LPush(ctx, rkeys.K.DriverLocationHistory(profile.ID), locJSON)
	s.redis.LTrim(ctx, rkeys.K.DriverLocationHistory(profile.ID), 0, 9)

	// Update Redis GEO index (skip if movement < 15m — noise filter)
	prevEntries, _ := s.redis.LRange(ctx, rkeys.K.DriverLocationHistory(profile.ID), 1, 1).Result()
	skipGeo := false
	if len(prevEntries) > 0 {
		var prev struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		}
		if json.Unmarshal([]byte(prevEntries[0]), &prev) == nil {
			prevPoint := geo.Point{Lat: prev.Lat, Lng: prev.Lng}
			if geo.DistanceKM(prevPoint, newPoint)*1000 < 15 {
				skipGeo = true
			}
		}
	}
	if !skipGeo {
		s.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(profile.TransportType), &goredis.GeoLocation{
			Name:      profile.ID,
			Longitude: update.Lng,
			Latitude:  update.Lat,
		})
	}

	_ = s.repo.UpsertLocation(ctx, profile.ID, newPoint, update.SpeedKMH, update.Heading)
	return nil
}

func (s *Service) checkGPSPlausibility(ctx context.Context, driverProfileID string, newPoint geo.Point) (bool, float64) {
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
func (s *Service) GetDailyEarnings(ctx context.Context, driverUserID string) (float64, error) {
	gross, err := s.repo.GetEarnings(ctx, driverUserID, "1 day")
	if err != nil {
		return 0, err
	}
	return CalculateDriverPayout(gross), nil
}

// GetWeeklyEarnings returns total fare revenue for the last 7 days.
func (s *Service) GetWeeklyEarnings(ctx context.Context, driverUserID string) (float64, error) {
	gross, err := s.repo.GetEarnings(ctx, driverUserID, "7 days")
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

// GetNearbyDrivers returns anonymised nearby drivers for a customer location.
func (s *Service) GetNearbyDrivers(ctx context.Context, loc geo.Point, transportType string) ([]*NearbyDriver, error) {
	candidates, err := s.repo.FindNearby(ctx, loc, 5000, transportType, nil)
	if err != nil {
		return nil, err
	}

	var result []*NearbyDriver
	for _, c := range candidates {
		result = append(result, &NearbyDriver{
			TransportType: c.TransportType,
			DistanceM:     c.DistanceM,
			ApproxLat:     c.Lat + jitter(),
			ApproxLng:     c.Lng + jitter(),
		})
	}
	return result, nil
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

func jitter() float64 {
	return (rand.Float64() - 0.5) * 0.006
}
