package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/workspace/ride-platform/pkg/timeutil"
)

type Config struct {
	Port        string
	Env         string
	AdminOrigin string // CORS allowed origin for admin frontend (production URL)
	// Timezone is the IANA name defining a calendar "day" platform-wide —
	// driver daily earnings, daily penalty counters, digests. Rwanda is UTC+2,
	// so leaving this at UTC would roll every daily figure over at 02:00 local.
	// PLATFORM_TIMEZONE — default Africa/Kigali.
	Timezone string
	// location caches the parsed Timezone; read it via Location().
	location *time.Location

	Database DatabaseConfig
	Redis    RedisConfig
	JWT      JWTConfig
	AT       ATConfig
	Pindo    PindoConfig
	// SMSProvider selects the SMS gateway: "africastalking" (default) or "pindo".
	SMSProvider string
	// OTPMode selects how phone OTP is done: "self_sms" (we generate+verify the
	// code, delivered via SMSProvider) or "pindo_verify" (Pindo's Verify API owns
	// the PIN lifecycle — cheaper, ~$0.002 per successful verification).
	OTPMode   string
	Firebase  FirebaseConfig
	Telegram  TelegramConfig
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

// Location returns the platform timezone that defines a calendar day. Load()
// resolves it once; a Config built directly in a test resolves on demand. Never
// nil, so callers can pass the result straight to timeutil.
func (c *Config) Location() *time.Location {
	if c == nil {
		return timeutil.Location("")
	}
	if c.location != nil {
		return c.location
	}
	return timeutil.Location(c.Timezone)
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
	/** Idle timeout for admin console sessions (renewed on activity). */
	AdminIdleMinutes     int
	AdminIdleExpiry      time.Duration
	AdminSessionMaxHours int
	AdminSessionMax      time.Duration
	/**
	 * How long the login 2FA step stays valid — the window to open an
	 * authenticator, scan a QR on first enrolment, and type a code. Separate from
	 * the dashboard session because it is a different job: this one is about
	 * finishing a sign-in, not staying signed in.
	 */
	AdminPreAuthMinutes int
	AdminPreAuthExpiry  time.Duration
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

// TelegramConfig drives operational alerting (pkg/alerting): error-level log
// events and deploy notices go to the team's Telegram group. Empty = disabled.
type TelegramConfig struct {
	BotToken string // TELEGRAM_BOT_TOKEN — from @BotFather
	ChatID   string // TELEGRAM_CHAT_ID — the team group's chat id

	// Daily operations digest (internal/digest). Pushed to the same chat each
	// morning so an ordinary day produces a message — silence then means the
	// API is down, rather than meaning nothing happened.
	DigestEnabled  bool   // DIGEST_ENABLED (default true when Telegram is configured)
	DigestHour     int    // DIGEST_HOUR — local hour 0–23 (default 7)
	DigestTimezone string // DIGEST_TIMEZONE — IANA name (default Africa/Kigali)
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
	// AccountID is the Cloudflare ACCOUNT ID used to build the R2 API host
	// (https://<AccountID>.r2.cloudflarestorage.com) when Provider=r2 and no
	// explicit Endpoint is set. R2's host uses the account id, NOT the key id.
	AccountID string
}

type MatchingConfig struct {
	PrimaryRadiusM  int
	ExpandedRadiusM int
	TimeoutSeconds  int
	MaxAttempts     int
	// WaveIntervalSeconds is how long to wait before widening the search after a
	// round finds nobody. It was effectively zero: the loop did a bare `continue`,
	// so all attempts completed in milliseconds and the ride was cancelled before
	// the HTTP response was even written. Waiting is the cheapest supply-side
	// lever there is — a driver who finishes a trip 30s from now is invisible to a
	// search that gave up instantly.
	WaveIntervalSeconds int
	// GiveUpSeconds caps the whole search. Previously no wall-clock deadline
	// existed anywhere, so the worst case was data-dependent and unbounded.
	GiveUpSeconds int
	// TierRadiiM is an explicit metre override for the broadcast bands, applied to
	// EVERY vehicle type. Leave it empty (the default) to derive bands per vehicle
	// from TierETAMinutes instead, which is almost always what you want — see
	// TierRadiiForVehicle.
	TierRadiiM []int
	// TierETAMinutes is the pickup promise, in minutes, for each broadcast band.
	// One promise for all vehicle types; the DISTANCE that satisfies it differs
	// per vehicle because their speeds differ. A Fuso at 800m is a ~4.5 minute
	// pickup while a moto at 800m is ~3, so a single metre figure quietly means a
	// different customer experience per vehicle.
	TierETAMinutes []int
	// VehicleSpeedKmh is the effective door-to-door speed per vehicle_types.code,
	// including traffic and Kigali's hills — not a vehicle's top speed.
	VehicleSpeedKmh map[string]float64
	// RoadDetourFactor converts straight-line distance (which is all Redis GEO
	// measures) into road distance. Kigali's winding, hilly streets with few
	// through-routes run about 1.4x crow-flies.
	RoadDetourFactor float64
	// BatchSize is how many drivers in a band are offered the ride AT ONCE, first
	// accept wins. Offers used to be strictly sequential with a 15s block each, so
	// four uninterested drivers cost a full minute before a fifth was tried.
	BatchSize int
	// TierWindowSeconds is how long a broadcast waits for any driver in the band
	// to accept before moving outward.
	TierWindowSeconds int
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
	// AbandonSilenceMinutes is how long a ride may sit in CONFIRMED /
	// DRIVER_EN_ROUTE / DRIVER_ARRIVED with a totally silent driver (location key
	// expired AND no live WebSocket) before the abandonment watchdog cancels it
	// as driver-fault. The location key's TTL is 120s, so 3 minutes means at
	// least one full TTL plus a missed heartbeat — a driver merely between GPS
	// ticks can't trip it.
	AbandonSilenceMinutes int
	// AbandonOnboardSilenceMinutes is the same threshold for IN_PROGRESS rides.
	// Longer, because a passenger is (nominally) aboard and tunnels / dead zones
	// mid-trip are normal; the dead-man finalizer remains the backstop.
	AbandonOnboardSilenceMinutes int
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
	cfg.Env = getEnv("ENV", "staging")
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
	// Admin console sessions are separate from the mobile app's: the console
	// renews on activity, so this is effectively an idle timeout. 15 minutes of
	// absolute expiry meant admins were bounced to login mid-task.
	cfg.JWT.AdminIdleMinutes = getEnvInt("JWT_ADMIN_IDLE_MINUTES", 60)
	// Hard ceiling on a renewed session, no matter how active: a stolen token
	// must not be renewable forever.
	// Absolute cap on a renewed admin session, counted from the original login.
	// 12h bounced admins mid-shift: with an 8h idle window, someone working a
	// normal day hit the ceiling and was thrown to the login screen with work in
	// progress. 24h keeps the idle timeout doing the real security work (plus
	// mandatory 2FA) while surviving a full day at the console.
	cfg.JWT.AdminSessionMaxHours = getEnvInt("JWT_ADMIN_SESSION_MAX_HOURS", 24)
	// The login 2FA window. Was a hardcoded 15 minutes, which is tight for a
	// first-time enrolment: install an authenticator, scan the QR, wait for a
	// fresh code. Running out mid-setup surfaced as "your sign-in session
	// expired", which reads like a bug rather than a timer.
	cfg.JWT.AdminPreAuthMinutes = getEnvInt("JWT_ADMIN_PREAUTH_MINUTES", 30)
	cfg.JWT.RefreshExpiryDays = getEnvInt("JWT_REFRESH_EXPIRY_DAYS", 30)
	cfg.JWT.AccessExpiry = time.Duration(cfg.JWT.AccessExpiryMinutes) * time.Minute
	cfg.JWT.AdminIdleExpiry = time.Duration(cfg.JWT.AdminIdleMinutes) * time.Minute
	cfg.JWT.AdminSessionMax = time.Duration(cfg.JWT.AdminSessionMaxHours) * time.Hour
	cfg.JWT.AdminPreAuthExpiry = time.Duration(cfg.JWT.AdminPreAuthMinutes) * time.Minute
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

	cfg.Telegram.BotToken = getEnv("TELEGRAM_BOT_TOKEN", "")
	cfg.Telegram.ChatID = getEnv("TELEGRAM_CHAT_ID", "")
	cfg.Telegram.DigestEnabled = getEnvBool("DIGEST_ENABLED", true)
	cfg.Telegram.DigestHour = getEnvInt("DIGEST_HOUR", 7)
	// DIGEST_TIMEZONE predates the platform-wide setting and stays honoured so
	// existing deployments keep working; PLATFORM_TIMEZONE is the one to set now.
	cfg.Timezone = getEnv("PLATFORM_TIMEZONE", timeutil.FallbackTimezone)
	cfg.location = timeutil.Location(cfg.Timezone)
	cfg.Telegram.DigestTimezone = getEnv("DIGEST_TIMEZONE", cfg.Timezone)

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

	cfg.Storage.Provider = getEnv("STORAGE_PROVIDER", "r2")
	cfg.Storage.Bucket = getEnv("STORAGE_BUCKET", "")
	cfg.Storage.Region = getEnv("STORAGE_REGION", "auto")
	cfg.Storage.KeyID = getEnv("STORAGE_KEY_ID", "")
	cfg.Storage.Secret = getEnv("STORAGE_SECRET", "")
	cfg.Storage.CDNURL = getEnv("STORAGE_CDN_URL", "")
	cfg.Storage.Endpoint = getEnv("STORAGE_ENDPOINT", "")
	cfg.Storage.AccountID = getEnv("STORAGE_ACCOUNT_ID", "")

	// Wave ladder: radius doubles per attempt from the primary and clamps at the
	// expanded radius — 2km → 4km → 8km → 10km. Starting at 2km targets drivers
	// ~5 minutes away (25 km/h city speed) before widening; waves fire at
	// 0/12/24/36s so the whole search resolves inside the 60s give-up budget.
	cfg.Matching.PrimaryRadiusM = getEnvInt("MATCH_RADIUS_PRIMARY_M", 2000)
	cfg.Matching.ExpandedRadiusM = getEnvInt("MATCH_RADIUS_EXPANDED_M", 10000)
	cfg.Matching.TimeoutSeconds = getEnvInt("MATCH_TIMEOUT_SECONDS", 15)
	cfg.Matching.MaxAttempts = getEnvInt("MATCH_MAX_ATTEMPTS", 4)
	cfg.Matching.WaveIntervalSeconds = getEnvInt("MATCH_WAVE_INTERVAL_SECONDS", 12)
	cfg.Matching.GiveUpSeconds = getEnvInt("MATCH_GIVE_UP_SECONDS", 45)
	cfg.Matching.TierRadiiM = getEnvIntList("MATCH_TIER_RADII_M", nil)
	cfg.Matching.TierETAMinutes = getEnvIntList("MATCH_TIER_ETA_MINUTES", []int{3, 6, 10})
	cfg.Matching.RoadDetourFactor = getEnvFloat("MATCH_ROAD_DETOUR_FACTOR", 1.4)
	cfg.Matching.VehicleSpeedKmh = map[string]float64{
		"MOTO_BIKE":   getEnvFloat("MATCH_SPEED_KMH_MOTO_BIKE", 22),
		"TUK_TUK":     getEnvFloat("MATCH_SPEED_KMH_TUK_TUK", 18),
		"LIGHT_HILUX": getEnvFloat("MATCH_SPEED_KMH_LIGHT_HILUX", 16),
		"CAB_TAXI":    getEnvFloat("MATCH_SPEED_KMH_CAB_TAXI", 16),
		"HEAVY_FUSO":  getEnvFloat("MATCH_SPEED_KMH_HEAVY_FUSO", 14),
	}
	cfg.Matching.BatchSize = getEnvInt("MATCH_BATCH_SIZE", 3)
	// 15s to match the driver app's hardcoded 15-second offer countdown. At 10s
	// the server moved on while the driver's screen still showed 5 seconds left,
	// so a tap in that gap got NOT_YOUR_OFFER for no visible reason.
	cfg.Matching.TierWindowSeconds = getEnvInt("MATCH_TIER_WINDOW_SECONDS", 15)

	cfg.Ride.StartRadiusM = getEnvInt("START_RIDE_RADIUS_M", 150)
	cfg.Ride.CompleteRadiusM = getEnvInt("COMPLETE_RIDE_RADIUS_M", 200)
	cfg.Ride.DevSkipGeofence = getEnvBool("DEV_SKIP_GEOFENCE", false)
	cfg.Ride.MaxInProgressMinutes = getEnvInt("RIDE_MAX_IN_PROGRESS_MINUTES", 120)
	cfg.Ride.NoShowVerifyRadiusM = getEnvInt("NO_SHOW_VERIFY_RADIUS_M", 400)
	cfg.Ride.AbandonSilenceMinutes = getEnvInt("RIDE_ABANDON_SILENCE_MINUTES", 3)
	cfg.Ride.AbandonOnboardSilenceMinutes = getEnvInt("RIDE_ABANDON_ONBOARD_SILENCE_MINUTES", 10)

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

// getEnvIntList parses a comma-separated list of ints, e.g. "800,1500,3000".
// Falls back wholesale on any malformed entry rather than silently dropping one:
// a half-parsed tier list would change matching behaviour in a way nobody would
// notice until pickups got slow.
// TierRadiiForVehicle returns the broadcast bands, in metres, for one vehicle
// type — derived from the ETA promise and that vehicle's effective speed.
//
// The bands used to be a single metre list shared by every vehicle, which meant
// the SAME distance implied a different wait depending on what the customer
// ordered: 800m is a ~3 minute moto pickup but a ~4.5 minute Fuso one. Deriving
// from ETA keeps the promise constant and lets the distance vary, which is the way
// round that matters to a passenger.
//
// Pure arithmetic on values fixed at load time — call it per search without
// touching Redis or Postgres.
//
// An explicit TierRadiiM wins, for when someone needs to pin exact metres.
func (m MatchingConfig) TierRadiiForVehicle(vehicleTypeCode string) []int {
	if len(m.TierRadiiM) > 0 {
		return m.TierRadiiM
	}
	etas := m.TierETAMinutes
	if len(etas) == 0 {
		etas = []int{3, 6, 10}
	}
	speed, ok := m.VehicleSpeedKmh[vehicleTypeCode]
	if !ok || speed <= 0 {
		// Unknown vehicle type: fall back to the slowest configured speed rather
		// than the fastest, so an unrecognised code cannot silently promise a
		// 3-minute pickup it has no chance of meeting.
		speed = 0
		for _, v := range m.VehicleSpeedKmh {
			if speed == 0 || (v > 0 && v < speed) {
				speed = v
			}
		}
		if speed <= 0 {
			speed = 14
		}
	}
	detour := m.RoadDetourFactor
	if detour < 1 {
		detour = 1.4
	}

	out := make([]int, 0, len(etas))
	for _, eta := range etas {
		if eta <= 0 {
			continue
		}
		roadM := speed * 1000.0 / 60.0 * float64(eta)
		straightM := roadM / detour
		out = append(out, int(straightM))
	}
	return out
}

func getEnvIntList(key string, fallback []int) []int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			return fallback
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return fallback
	}
	return out
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
