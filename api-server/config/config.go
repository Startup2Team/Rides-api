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

	Database  DatabaseConfig
	Redis     RedisConfig
	JWT       JWTConfig
	AT        ATConfig
	Pindo     PindoConfig
	// SMSProvider selects the SMS gateway: "africastalking" (default) or "pindo".
	SMSProvider string
	// OTPMode selects how phone OTP is done: "self_sms" (we generate+verify the
	// code, delivered via SMSProvider) or "pindo_verify" (Pindo's Verify API owns
	// the PIN lifecycle — cheaper, ~$0.002 per successful verification).
	OTPMode   string
	Firebase  FirebaseConfig
	GMaps     GoogleMapsConfig
	MoMo      MoMoConfig
	Storage   StorageConfig
	Matching  MatchingConfig
	Ride      RideConfig
	GPS       GPSConfig
	Driver    DriverConfig
	Customer  CustomerConfig
	Penalty   PenaltyConfig
	Payments  PaymentsConfig
	Security  SecurityConfig
	Analytics struct {
		BatchSize int
	}
}

// SecurityConfig holds API-protection tunables.
type SecurityConfig struct {
	// GlobalRateLimitPerMin caps requests per client IP per minute across all
	// routes (DDoS / abuse backstop). Higher than the old hard-coded 100 so that
	// many users behind one carrier-grade-NAT IP don't share a tiny bucket.
	GlobalRateLimitPerMin int
	// AuthRefreshRateLimit caps auth token refresh attempts.
	AuthRefreshRateLimit int
	// MomoWebhookRateLimit caps MTN MoMo webhook callback requests.
	MomoWebhookRateLimit int
	// AdminLoginRateLimit caps administrative login/2FA attempts.
	AdminLoginRateLimit int
	// DriverLocationRateLimit caps driver location updates per user.
	DriverLocationRateLimit int
	// MaxRequestBodyBytes caps non-upload request bodies (memory-exhaustion guard).
	MaxRequestBodyBytes int64
	// SwaggerEnabled exposes /swagger. Off by default in production.
	SwaggerEnabled bool
	// SwaggerBasicAuth, when "user:pass", protects /swagger with HTTP Basic auth.
	SwaggerBasicAuth string
}

// PaymentsConfig gates real-money wallet movement. Until a payment gateway
// (MoMo collect/disburse) is wired and verified, top-up and withdraw MUST stay
// disabled — otherwise a user could mint wallet balance with no payment captured.
type PaymentsConfig struct {
	Enabled bool
	// WebhookSecret gates the public MoMo callback. When set, callbacks must
	// present it in the X-Webhook-Secret header (constant-time compared). Empty
	// disables the check (dev only) — it MUST be set in production.
	WebhookSecret string

	// Manual-payment instructions shown to riders who pay off-platform (send
	// MoMo to the merchant number, then submit proof for admin verification).
	ManualMomoCode     string // e.g. "*182*8*1*123456#" or a merchant number
	ManualMomoName     string // merchant/account name to confirm against
	ManualInstructions string // free-text steps shown in the app
}

type DatabaseConfig struct {
	URL      string
	ReadURL  string
	MaxConns int
	MinConns int
}

type RedisConfig struct {
	URL         string
	ClusterMode bool
}

type JWTConfig struct {
	AccessSecret        string
	AdminAccessSecret   string
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
	// WhatsApp fields — optional, dev convenience only.
	// Set AT_WHATSAPP_ENABLED=true + AT_WHATSAPP_SENDER to a registered WA number.
	WhatsAppEnabled bool
	WhatsAppSender  string
}

// PindoConfig holds Pindo (pindo.io) SMS credentials — the cheaper Rwanda-local
// alternative to Africa's Talking. Used when SMSProvider == "pindo".
type PindoConfig struct {
	APIToken string // Bearer token from the Pindo dashboard
	Sender   string // approved Sender ID (e.g. "Rides")
	Brand    string // brand name shown in Verify (2FA) messages — PINDO_VERIFY_BRAND
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
	WebhookSecret   string
	IPWhitelist     string

	// Live MTN MoMo Collections credentials. When APIUser + APIKey +
	// SubscriptionKey are all set, the payment service makes real RequestToPay
	// calls; otherwise it stays inert (returns a mock PENDING) so the rest of
	// the flow keeps working in dev / before provisioning.
	APIUser     string // MOMO_API_USER — the provisioned API user UUID
	BaseURL     string // MOMO_BASE_URL — override; defaults by Environment
	Currency    string // MOMO_CURRENCY — "EUR" in sandbox, "RWF" in production
	CallbackURL string // MOMO_CALLBACK_URL — optional X-Callback-Url for RequestToPay
}

type StorageConfig struct {
	Provider string
	Bucket   string
	Region   string
	KeyID    string
	Secret   string
	CDNURL   string
	// Endpoint overrides the S3 API host for S3-compatible stores (MinIO in dev,
	// or any self-hosted gateway). Empty = real AWS S3 (default endpoints).
	Endpoint string
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
	// DevSkipGeofence bypasses arrival/start/complete radius checks.
	// NEVER set true in production.
	DevSkipGeofence bool
	// MaxInProgressMinutes is how long a ride may stay IN_PROGRESS before the
	// dead-man finalizer auto-completes it (driver abandoned / went offline).
	MaxInProgressMinutes int
	// NoShowVerifyRadiusM: a "customer no-show" refund is only honoured if the
	// driver's last-known location is still within this radius of the pickup. If
	// they've driven off (toward the destination), the no-show is treated as
	// unverified — no refund, and the ride is flagged.
	NoShowVerifyRadiusM int
}

type GPSConfig struct {
	MaxSpeedKMH           float64
	StaleThresholdSeconds float64 // skip plausibility check if previous entry is older than this
}

type DriverConfig struct {
	OfflineCooldownMinutes      int
	DeclinePriorityThreshold    int
	DeclineAutoOfflineThreshold int
	DevAutoApprove              bool // DEV ONLY: skip admin approval on driver registration
	// CancelWarnThreshold / CancelBanThreshold: daily cancels at which a driver
	// is warned, then temporarily banned.
	CancelWarnThreshold int
	CancelBanThreshold  int
}

type CustomerConfig struct {
	CancelWarnThreshold    int
	CancelSuspendThreshold int
	CancelSuspendHours     int
	// CancelBanThreshold: daily cancels at which a customer is temp-banned.
	CancelBanThreshold int
}

// PenaltyConfig holds the shared cancellation-penalty escalation knobs.
type PenaltyConfig struct {
	// BanHours is how long a temporary cancellation ban lasts.
	BanHours int
	// BansBeforeSuspend: once a user has had this many temp-bans, the next
	// threshold breach is an indefinite suspension instead of another temp-ban.
	BansBeforeSuspend int
}

func Load() (*Config, error) {
	// Load .env if present (no-op in production if file missing)
	_ = godotenv.Load()

	cfg := &Config{}

	cfg.Port = getEnv("PORT", "8080")
	cfg.Env = getEnv("ENV", "development")
	cfg.AdminOrigin = getEnv("ADMIN_ORIGIN", "")

	cfg.Database.URL = requireEnv("DATABASE_URL")
	cfg.Database.ReadURL = getEnv("DATABASE_READ_URL", "")
	if cfg.Database.ReadURL == "" {
		cfg.Database.ReadURL = cfg.Database.URL
	}
	// Default pool sized for a single strong instance. When running MULTIPLE api
	// instances (horizontal scale), put PgBouncer in front and lower per-instance
	// MaxConns so N_instances × MaxConns stays under Postgres max_connections.
	cfg.Database.MaxConns = getEnvInt("DATABASE_MAX_CONNS", 60)
	cfg.Database.MinConns = getEnvInt("DATABASE_MIN_CONNS", 10)
	cfg.Redis.URL = getEnv("REDIS_URL", "redis://localhost:6379")
	cfg.Redis.ClusterMode = getEnvBool("REDIS_CLUSTER_MODE", false)

	cfg.JWT.AccessSecret = requireEnv("JWT_ACCESS_SECRET")
	cfg.JWT.AdminAccessSecret = getEnv("JWT_ADMIN_ACCESS_SECRET", "")
	if cfg.JWT.AdminAccessSecret == "" {
		cfg.JWT.AdminAccessSecret = cfg.JWT.AccessSecret
	}
	cfg.JWT.RefreshSecret = requireEnv("JWT_REFRESH_SECRET")
	cfg.JWT.AccessExpiryMinutes = getEnvInt("JWT_ACCESS_EXPIRY_MINUTES", 15)
	cfg.JWT.RefreshExpiryDays = getEnvInt("JWT_REFRESH_EXPIRY_DAYS", 30)
	cfg.JWT.AccessExpiry = time.Duration(cfg.JWT.AccessExpiryMinutes) * time.Minute
	cfg.JWT.RefreshExpiry = time.Duration(cfg.JWT.RefreshExpiryDays) * 24 * time.Hour

	cfg.AT.APIKey = getEnv("AT_API_KEY", "")
	cfg.AT.Username = getEnv("AT_USERNAME", "")
	cfg.AT.SenderID = getEnv("AT_SENDER_ID", "")
	cfg.AT.MaskingNumber = getEnv("AT_MASKING_NUMBER", "")
	cfg.AT.WhatsAppEnabled = getEnvBool("AT_WHATSAPP_ENABLED", false)
	cfg.AT.WhatsAppSender = getEnv("AT_WHATSAPP_SENDER", "")

	cfg.SMSProvider = getEnv("SMS_PROVIDER", "africastalking")
	cfg.Pindo.APIToken = getEnv("PINDO_API_TOKEN", "")
	cfg.Pindo.Sender = getEnv("PINDO_SENDER", "")
	cfg.Pindo.Brand = getEnv("PINDO_VERIFY_BRAND", "Rides")
	cfg.OTPMode = getEnv("OTP_MODE", "self_sms")

	cfg.Firebase.ServiceAccountPath = getEnv("FIREBASE_SERVICE_ACCOUNT_PATH", "./firebase-service-account.json")

	cfg.GMaps.APIKey = getEnv("GOOGLE_MAPS_API_KEY", "")

	cfg.MoMo.APIKey = getEnv("MOMO_API_KEY", "")
	cfg.MoMo.SubscriptionKey = getEnv("MOMO_SUBSCRIPTION_KEY", "")
	cfg.MoMo.Environment = getEnv("MOMO_ENVIRONMENT", "sandbox")
	cfg.MoMo.WebhookSecret = getEnv("MOMO_WEBHOOK_SECRET", "")
	cfg.MoMo.IPWhitelist = getEnv("MOMO_IP_WHITELIST", "")
	cfg.MoMo.APIUser = getEnv("MOMO_API_USER", "")
	cfg.MoMo.BaseURL = getEnv("MOMO_BASE_URL", "")
	cfg.MoMo.Currency = getEnv("MOMO_CURRENCY", "")
	cfg.MoMo.CallbackURL = getEnv("MOMO_CALLBACK_URL", "")

	cfg.Storage.Provider = getEnv("STORAGE_PROVIDER", "s3")
	cfg.Storage.Bucket = getEnv("STORAGE_BUCKET", "")
	cfg.Storage.Region = getEnv("STORAGE_REGION", "auto")
	cfg.Storage.KeyID = getEnv("STORAGE_KEY_ID", "")
	cfg.Storage.Secret = getEnv("STORAGE_SECRET", "")
	cfg.Storage.CDNURL = getEnv("STORAGE_CDN_URL", "")
	cfg.Storage.Endpoint = getEnv("STORAGE_ENDPOINT", "")

	cfg.Matching.PrimaryRadiusM = getEnvInt("MATCH_RADIUS_PRIMARY_M", 5000)
	cfg.Matching.ExpandedRadiusM = getEnvInt("MATCH_RADIUS_EXPANDED_M", 10000)
	cfg.Matching.TimeoutSeconds = getEnvInt("MATCH_TIMEOUT_SECONDS", 15)
	cfg.Matching.MaxAttempts = getEnvInt("MATCH_MAX_ATTEMPTS", 3)

	cfg.Ride.StartRadiusM = getEnvInt("START_RIDE_RADIUS_M", 150)
	cfg.Ride.CompleteRadiusM = getEnvInt("COMPLETE_RIDE_RADIUS_M", 200)
	cfg.Ride.DevSkipGeofence = getEnvBool("DEV_SKIP_GEOFENCE", false)
	cfg.Ride.MaxInProgressMinutes = getEnvInt("RIDE_MAX_IN_PROGRESS_MINUTES", 120)
	cfg.Ride.NoShowVerifyRadiusM = getEnvInt("NO_SHOW_VERIFY_RADIUS_M", 400)

	cfg.GPS.MaxSpeedKMH = getEnvFloat("GPS_MAX_SPEED_KMH", 200.0)
	cfg.GPS.StaleThresholdSeconds = getEnvFloat("GPS_STALE_THRESHOLD_SECONDS", 300.0)

	cfg.Driver.OfflineCooldownMinutes = getEnvInt("DRIVER_OFFLINE_COOLDOWN_MINUTES", 10)
	cfg.Driver.DeclinePriorityThreshold = getEnvInt("DRIVER_DECLINE_PRIORITY_THRESHOLD", 10)
	cfg.Driver.DeclineAutoOfflineThreshold = getEnvInt("DRIVER_DECLINE_AUTO_OFFLINE_THRESHOLD", 15)
	cfg.Driver.DevAutoApprove = getEnvBool("DEV_AUTO_APPROVE_DRIVERS", false)

	cfg.Customer.CancelWarnThreshold = getEnvInt("CUSTOMER_CANCEL_WARN_THRESHOLD", 4)
	cfg.Customer.CancelSuspendThreshold = getEnvInt("CUSTOMER_CANCEL_SUSPEND_THRESHOLD", 8)
	cfg.Customer.CancelSuspendHours = getEnvInt("CUSTOMER_CANCEL_SUSPEND_HOURS", 2)
	cfg.Customer.CancelBanThreshold = getEnvInt("CUSTOMER_CANCEL_BAN_THRESHOLD", 5)

	// Driver cancellation penalties: warn at 3/day, temp-ban at 4/day.
	cfg.Driver.CancelWarnThreshold = getEnvInt("DRIVER_CANCEL_WARN_THRESHOLD", 3)
	cfg.Driver.CancelBanThreshold = getEnvInt("DRIVER_CANCEL_BAN_THRESHOLD", 4)

	// Shared escalation: a temp-ban lasts 24h; the 5th ban becomes a suspension.
	cfg.Penalty.BanHours = getEnvInt("PENALTY_BAN_HOURS", 24)
	cfg.Penalty.BansBeforeSuspend = getEnvInt("PENALTY_BANS_BEFORE_SUSPEND", 5)

	// Real-money wallet movement stays OFF until a verified payment gateway exists.
	cfg.Payments.Enabled = getEnvBool("PAYMENTS_ENABLED", false)
	cfg.Payments.WebhookSecret = getEnv("MOMO_WEBHOOK_SECRET", "")
	cfg.Payments.ManualMomoCode = getEnv("MANUAL_PAY_MOMO_CODE", "")
	cfg.Payments.ManualMomoName = getEnv("MANUAL_PAY_MOMO_NAME", "")
	cfg.Payments.ManualInstructions = getEnv("MANUAL_PAY_INSTRUCTIONS", "")

	// 1200/min (20/s) per IP: a loose DDoS/abuse backstop only. Real per-actor
	// throttling is done per-user (JWT) on the authed groups, so a whole carrier
	// NAT of legitimate users sharing one IP won't be throttled by this.
	cfg.Security.GlobalRateLimitPerMin = getEnvInt("GLOBAL_RATE_LIMIT_PER_MIN", 1200)
	cfg.Security.AuthRefreshRateLimit = getEnvInt("RATE_LIMIT_AUTH_REFRESH", 20)
	cfg.Security.MomoWebhookRateLimit = getEnvInt("RATE_LIMIT_MOMO_WEBHOOK", 120)
	cfg.Security.AdminLoginRateLimit = getEnvInt("RATE_LIMIT_ADMIN_LOGIN", 5)
	cfg.Security.DriverLocationRateLimit = getEnvInt("RATE_LIMIT_DRIVER_LOCATION", 20)
	cfg.Security.MaxRequestBodyBytes = int64(getEnvInt("MAX_REQUEST_BODY_BYTES", 1<<20)) // 1 MiB
	cfg.Security.SwaggerEnabled = getEnvBool("SWAGGER_ENABLED", cfg.Env != "production")
	cfg.Security.SwaggerBasicAuth = getEnv("SWAGGER_BASIC_AUTH", "")

	cfg.Analytics.BatchSize = getEnvInt("ANALYTICS_BATCH_SIZE", 100)

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

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
