package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// New parses REDIS_URL and returns a connected Redis client.
func New(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}

	client := redis.NewClient(opts)

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping: %w", err)
	}

	return client, nil
}

// Keys is the single source of truth for all Redis key patterns.
// Every key is constructed through a function here — no ad-hoc key strings elsewhere.
type Keys struct{}

var K Keys

// ── Driver location (hot path) ────────────────────────────────────────────

func (Keys) DriverLocation(driverID string) string {
	return fmt.Sprintf("driver:location:%s", driverID)
}

func (Keys) DriverLocationHistory(driverID string) string {
	return fmt.Sprintf("driver:location:%s:history", driverID)
}

// ── Driver state ──────────────────────────────────────────────────────────

// DriverState stores AVAILABLE | ON_TRIP | OFFLINE
func (Keys) DriverState(driverID string) string {
	return fmt.Sprintf("driver:%s:state", driverID)
}

// DriverActiveRide stores the rideID the driver is currently on
func (Keys) DriverActiveRide(driverID string) string {
	return fmt.Sprintf("driver:%s:active_ride", driverID)
}

// DriverGeoIndex is the Redis GEO sorted set for a vehicle type
// e.g. drivers:geo:MOTO_BIKE
func (Keys) DriverGeoIndex(vehicleType string) string {
	return fmt.Sprintf("drivers:geo:%s", vehicleType)
}

// ── Matching ──────────────────────────────────────────────────────────────

// MatchingLock is SET NX per driver — prevents two rides grabbing the same driver
func (Keys) MatchingLock(driverID string) string {
	return fmt.Sprintf("matching:lock:%s", driverID)
}

func (Keys) RidePendingDriver(rideID string) string {
	return fmt.Sprintf("ride:%s:pending_driver", rideID)
}

func (Keys) RideExcludedDrivers(rideID string) string {
	return fmt.Sprintf("ride:%s:excluded_drivers", rideID)
}

// ── Ride state cache ──────────────────────────────────────────────────────

func (Keys) RideState(rideID string) string {
	return fmt.Sprintf("ride:%s:state", rideID)
}

func (Keys) CustomerActiveRide(customerID string) string {
	return fmt.Sprintf("customer:%s:active_ride", customerID)
}

// ── Negotiation ───────────────────────────────────────────────────────────

func (Keys) RideNegotiation(rideID string) string {
	return fmt.Sprintf("ride:%s:negotiation", rideID)
}

func (Keys) RideOfferCount(rideID, role string) string {
	return fmt.Sprintf("ride:%s:offers:%s", rideID, role)
}

// ── Driver penalties ──────────────────────────────────────────────────────

func (Keys) DriverDailyDeclines(driverID string) string {
	return fmt.Sprintf("driver:penalties:%s:daily_declines", driverID)
}

func (Keys) DriverOfflineAt(driverID string) string {
	return fmt.Sprintf("driver:penalties:%s:offline_at", driverID)
}

// ── Customer ──────────────────────────────────────────────────────────────

func (Keys) CustomerDailyCancel(customerID string) string {
	return fmt.Sprintf("customer:cancels:%s:daily", customerID)
}

// ── Auth sessions ─────────────────────────────────────────────────────────

func (Keys) Session(userID, jti string) string {
	return fmt.Sprintf("session:%s:%s", userID, jti)
}

func (Keys) OTPRateLimit(phone string) string {
	return fmt.Sprintf("ratelimit:otp:%s", phone)
}

// ── Route cache ───────────────────────────────────────────────────────────

// RouteCache is the Redis hot-path key for a cached route (TTL 24h)
func (Keys) RouteCache(cacheKey string) string {
	return fmt.Sprintf("route:%s", cacheKey)
}

// ── Suggestions ───────────────────────────────────────────────────────────

func (Keys) UserSuggestions(userID string) string {
	return fmt.Sprintf("suggestions:%s", userID)
}

func (Keys) LandmarkSuggestions() string {
	return "landmark:suggestions"
}

// ── GPS anomaly ───────────────────────────────────────────────────────────

func (Keys) GPSAnomalyCount(driverID string) string {
	return fmt.Sprintf("driver:gps_anomalies:%s:session_count", driverID)
}

// ── Analytics ─────────────────────────────────────────────────────────────

func (Keys) AnalyticsStream() string {
	return "analytics:events"
}

// ── Admin dashboard ────────────────────────────────────────────────────────

func (Keys) DashboardCache() string {
	return "admin:dashboard:cache"
}

func (Keys) RevenueTodayCache() string {
	return "admin:revenue:today"
}
