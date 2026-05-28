package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	Env         string
	AdminOrigin string // CORS allowed origin for admin frontend (production URL)

	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	AT       ATConfig
	Firebase FirebaseConfig
	GMaps    GoogleMapsConfig
	MoMo     MoMoConfig
	Matching MatchingConfig
	Ride     RideConfig
	GPS      GPSConfig
	Driver   DriverConfig
	Customer CustomerConfig
}

type DatabaseConfig struct {
	URL string
}

type RedisConfig struct {
	URL string
}

type JWTConfig struct {
	AccessSecret        string
	RefreshSecret       string
	AccessExpiryMinutes int
	RefreshExpiryDays   int
	AccessExpiry        time.Duration
	RefreshExpiry       time.Duration
}

type ATConfig struct {
	APIKey        string
	Username      string
	SenderID      string
	MaskingNumber string
}

type FirebaseConfig struct {
	ServiceAccountPath string
}

type GoogleMapsConfig struct {
	APIKey string
}

type MoMoConfig struct {
	APIKey          string
	SubscriptionKey string
	Environment     string
}

type MatchingConfig struct {
	PrimaryRadiusM  int
	ExpandedRadiusM int
	TimeoutSeconds  int
	MaxAttempts     int
}

type RideConfig struct {
	StartRadiusM    int
	CompleteRadiusM int
}

type GPSConfig struct {
	MaxSpeedKMH float64
}

type DriverConfig struct {
	OfflineCooldownMinutes      int
	DeclinePriorityThreshold    int
	DeclineAutoOfflineThreshold int
}

type CustomerConfig struct {
	CancelWarnThreshold    int
	CancelSuspendThreshold int
	CancelSuspendHours     int
}

func Load() (*Config, error) {
	// Load .env if present (no-op in production if file missing)
	_ = godotenv.Load()

	cfg := &Config{}

	cfg.Port = getEnv("PORT", "8080")
	cfg.Env = getEnv("ENV", "development")
	cfg.AdminOrigin = getEnv("ADMIN_ORIGIN", "")

	cfg.Database.URL = requireEnv("DATABASE_URL")
	cfg.Redis.URL = getEnv("REDIS_URL", "redis://localhost:6379")

	cfg.JWT.AccessSecret = requireEnv("JWT_ACCESS_SECRET")
	cfg.JWT.RefreshSecret = requireEnv("JWT_REFRESH_SECRET")
	cfg.JWT.AccessExpiryMinutes = getEnvInt("JWT_ACCESS_EXPIRY_MINUTES", 15)
	cfg.JWT.RefreshExpiryDays = getEnvInt("JWT_REFRESH_EXPIRY_DAYS", 30)
	cfg.JWT.AccessExpiry = time.Duration(cfg.JWT.AccessExpiryMinutes) * time.Minute
	cfg.JWT.RefreshExpiry = time.Duration(cfg.JWT.RefreshExpiryDays) * 24 * time.Hour

	cfg.AT.APIKey = getEnv("AT_API_KEY", "")
	cfg.AT.Username = getEnv("AT_USERNAME", "")
	cfg.AT.SenderID = getEnv("AT_SENDER_ID", "")
	cfg.AT.MaskingNumber = getEnv("AT_MASKING_NUMBER", "")

	cfg.Firebase.ServiceAccountPath = getEnv("FIREBASE_SERVICE_ACCOUNT_PATH", "./firebase-service-account.json")

	cfg.GMaps.APIKey = getEnv("GOOGLE_MAPS_API_KEY", "")

	cfg.MoMo.APIKey = getEnv("MOMO_API_KEY", "")
	cfg.MoMo.SubscriptionKey = getEnv("MOMO_SUBSCRIPTION_KEY", "")
	cfg.MoMo.Environment = getEnv("MOMO_ENVIRONMENT", "sandbox")

	cfg.Matching.PrimaryRadiusM = getEnvInt("MATCH_RADIUS_PRIMARY_M", 5000)
	cfg.Matching.ExpandedRadiusM = getEnvInt("MATCH_RADIUS_EXPANDED_M", 10000)
	cfg.Matching.TimeoutSeconds = getEnvInt("MATCH_TIMEOUT_SECONDS", 15)
	cfg.Matching.MaxAttempts = getEnvInt("MATCH_MAX_ATTEMPTS", 3)

	cfg.Ride.StartRadiusM = getEnvInt("START_RIDE_RADIUS_M", 150)
	cfg.Ride.CompleteRadiusM = getEnvInt("COMPLETE_RIDE_RADIUS_M", 200)

	cfg.GPS.MaxSpeedKMH = getEnvFloat("GPS_MAX_SPEED_KMH", 200.0)

	cfg.Driver.OfflineCooldownMinutes = getEnvInt("DRIVER_OFFLINE_COOLDOWN_MINUTES", 10)
	cfg.Driver.DeclinePriorityThreshold = getEnvInt("DRIVER_DECLINE_PRIORITY_THRESHOLD", 10)
	cfg.Driver.DeclineAutoOfflineThreshold = getEnvInt("DRIVER_DECLINE_AUTO_OFFLINE_THRESHOLD", 15)

	cfg.Customer.CancelWarnThreshold = getEnvInt("CUSTOMER_CANCEL_WARN_THRESHOLD", 5)
	cfg.Customer.CancelSuspendThreshold = getEnvInt("CUSTOMER_CANCEL_SUSPEND_THRESHOLD", 8)
	cfg.Customer.CancelSuspendHours = getEnvInt("CUSTOMER_CANCEL_SUSPEND_HOURS", 2)

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return fallback
}
